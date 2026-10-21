"""k6 压测编排器：起 stub 上游 + 被测服务 → 跑场景 → 采内存 → 出报告 → 基线对比。

一次 `python perf/harness.py` 完成规划书里的「方案→执行→归因」执行段：
  1. 启动 tests/upstream_stub.py（六源 stub），按场景注入延迟画像/故障；
  2. 现场编译并启动被测 Go 服务（六源全指向 stub、独立 CACHE_DIR、
     SPELLBOOK_MIN_NAME_DELAY=10ms——stub 不会限流，无需生产节奏）；
  3. 运行 k6 场景脚本（阈值即闸门，退出码非 0 即失败）；
  4. 采样被测服务进程 RSS（缓存膨胀监控），k6 --summary-export 落盘；
  5. 与 perf/baseline.json 对比，P95 回归超过阈值（默认 25%）报失败；
     --update-baseline 用本次结果刷新基线。

用法：
  python perf/harness.py                     # 三个场景全跑 + 基线对比
  python perf/harness.py --scenario cache    # 单场景
  python perf/harness.py --scenario cold --fault   # 冷启动软降级画像
  python perf/harness.py --update-baseline   # 刷新基线
"""

from __future__ import annotations

import argparse
import datetime as _dt
import os
import json
import pathlib
import shutil
import subprocess
import sys
import tempfile
import threading
import time

REPO_ROOT = pathlib.Path(__file__).resolve().parents[1]
sys.path.insert(0, str(REPO_ROOT / "tests"))

from helpers import build_server_binary, free_port, stop_process, wait_ready  # noqa: E402
from upstream_stub import start_stub_pool  # noqa: E402

RESULTS_DIR = REPO_ROOT / "perf" / "results"
BASELINE_PATH = REPO_ROOT / "perf" / "baseline.json"

# 延迟画像（ms）：模拟真实上游的响应节奏。fault 画像让 salt 慢到超过被收缩的
# PROVIDER_TIMEOUT，验证分源超时软降级（partial 而非 5xx）。
PROFILES = {
    "nominal": {
        "moxfield": {"delay_ms": 400},
        "salt": {"delay_ms": 350},
        "scryfall_collection": {"delay_ms": 250},
        "edhrec": {"delay_ms": 300},
        "spellbook": {"delay_ms": 30},
        "getcards": {"delay_ms": 200},
    },
    "fault": {
        "moxfield": {"delay_ms": 400},
        "salt": {"delay_ms": 3000},
        "scryfall_collection": {"delay_ms": 250},
        "edhrec": {"delay_ms": 300},
        "spellbook": {"delay_ms": 30},
        "getcards": {"delay_ms": 200},
    },
    # 去重场景把编排拉长到 ~1.5s，确保 50 个并发都落进同一个在途 flight
    "dedup": {
        "moxfield": {"delay_ms": 600},
        "salt": {"delay_ms": 500},
        "scryfall_collection": {"delay_ms": 400},
        "edhrec": {"delay_ms": 450},
        "spellbook": {"delay_ms": 50},
        "getcards": {"delay_ms": 300},
    },
}


class StubControl:
    def __init__(self, base_url: str):
        import requests

        self.base = base_url.rstrip("/")
        self.http = requests.Session()

    def configure(self, behavior: dict) -> None:
        self.http.post(self.base + "/__configure", json=behavior, timeout=5)

    def reset(self) -> None:
        self.http.post(self.base + "/__reset", timeout=5)

    def stats(self) -> dict:
        return self.http.get(self.base + "/__stats", timeout=5).json()["counts"]


def sample_rss_windows(pid: int) -> list[int]:
    """跨平台读进程 RSS（KB）。读不到返回空表（内存监控尽力而为）。"""
    try:
        if sys.platform == "win32":
            out = subprocess.run(
                ["tasklist", "/FI", f"PID eq {pid}", "/FO", "CSV", "/NH"],
                capture_output=True, text=True, timeout=5,
            ).stdout.strip()
            if out and '"' in out:
                # "name","pid","session","num","mem"
                fields = [f.strip('"').replace(",", "") for f in out.split('","')]
                for field in reversed(fields):
                    digits = "".join(ch for ch in field if ch.isdigit())
                    if digits:
                        return int(digits)
        else:
            for line in pathlib.Path(f"/proc/{pid}/status").read_text().splitlines():
                if line.startswith("VmRSS:"):
                    return int(line.split()[1])
    except Exception:
        pass
    return []


class RSSSampler(threading.Thread):
    """压测期间每 0.5s 采样被测服务 RSS，报告峰值（缓存膨胀监控）。"""

    def __init__(self, pid: int):
        super().__init__(daemon=True)
        self.pid = pid
        self.stop_event = threading.Event()
        self.samples: list[int] = []

    def run(self) -> None:
        while not self.stop_event.is_set():
            rss = sample_rss_windows(self.pid)
            if rss:
                self.samples.append(rss)
            self.stop_event.wait(0.5)

    def stop(self) -> list[int]:
        self.stop_event.set()
        self.join(timeout=3)
        return self.samples


def metric_values(summary: dict, name: str) -> dict:
    """k6 --summary-export 是扁平结构：metrics.<name> 直接就是指标值字典。"""
    entry = summary.get("metrics", {}).get(name)
    return dict(entry) if entry else {}


def resolve_k6(explicit: str = "k6") -> str | None:
    """解析 k6 可执行文件：显式路径 → PATH → winget 默认安装位置。"""
    if pathlib.Path(explicit).exists():
        return explicit
    found = shutil.which(explicit)
    if found:
        return found
    for candidate in (
        pathlib.Path(os.environ.get("ProgramFiles", r"C:\Program Files")) / "k6" / "k6.exe",
        pathlib.Path.home() / "AppData" / "Local" / "Microsoft" / "WinGet" / "Links" / "k6.exe",
    ):
        if candidate.exists():
            return str(candidate)
    return None


def run_k6(script: pathlib.Path, env: dict, timeout: float = 600, k6_path: str = "k6") -> tuple[int, dict]:
    """跑一个 k6 场景，返回 (exit_code, summary-export 解析结果)。"""
    stamp = _dt.datetime.now().strftime("%Y%m%d-%H%M%S")
    export = RESULTS_DIR / f"{script.stem}-{stamp}.json"
    RESULTS_DIR.mkdir(parents=True, exist_ok=True)
    cmd = [k6_path, "run", "--summary-export", str(export)]
    for key, value in env.items():
        cmd += ["-e", f"{key}={value}"]
    cmd.append(str(script))
    print(f"  $ {' '.join(cmd)}")
    proc = subprocess.run(cmd, cwd=REPO_ROOT, capture_output=True, text=True, timeout=timeout, encoding="utf-8", errors="replace")
    if proc.returncode != 0:
        print(proc.stdout[-3000:])
        print(proc.stderr[-2000:])
    summary = json.loads(export.read_text(encoding="utf-8")) if export.exists() else {}
    return proc.returncode, summary


def scenario_cold(stub: StubControl, base_url: str, args, k6_path: str) -> dict:
    print("\n[1/3] 场景① 冷启动全源编排（唯一 deck id 强制冷路径）")
    profile = PROFILES["fault"] if args.fault else PROFILES["nominal"]
    stub.reset()
    stub.configure(profile)
    code, summary = run_k6(
        REPO_ROOT / "perf" / "cold-orchestration.k6.js",
        {"BASE_URL": base_url, "VUS": str(args.vus), "DURATION": args.duration, "RUN_STAMP": _run_stamp(), **({"FAULT": "salt"} if args.fault else {})},
        k6_path=k6_path,
    )
    return {
        "exit_code": code,
        "p50_ms": metric_values(summary, "http_req_duration").get("med"),
        "p95_ms": metric_values(summary, "http_req_duration").get("p(95)"),
        "p99_ms": metric_values(summary, "http_req_duration").get("p(99)"),
        "rps": metric_values(summary, "http_reqs").get("rate"),
        "error_rate": metric_values(summary, "http_req_failed").get("value"),
        "partial_count": metric_values(summary, "analyze_partial").get("count", 0),
        "success_count": metric_values(summary, "analyze_success").get("count", 0),
    }


def scenario_cache(stub: StubControl, base_url: str, args, k6_path: str) -> dict:
    print("\n[2/3] 场景② 缓存命中 vs 冷查询（收益曲线）")
    stub.reset()
    stub.configure(PROFILES["nominal"])
    deck_id = f"cache{_run_stamp()}"
    code, summary = run_k6(
        REPO_ROOT / "perf" / "cache-hit-cold.k6.js",
        {"BASE_URL": base_url, "DECK_ID": deck_id, "VUS": str(args.vus), "DURATION": args.duration},
        k6_path=k6_path,
    )
    warm = metric_values(summary, "http_req_duration{cache:warm}")
    cold = metric_values(summary, "analyze_cold_duration")
    return {
        "exit_code": code,
        "cold_ms": cold.get("max"),
        "warm_p50_ms": warm.get("med"),
        "warm_p95_ms": warm.get("p(95)"),
        "warm_p99_ms": warm.get("p(99)"),
        "rps": metric_values(summary, "http_reqs").get("rate"),
        "error_rate": metric_values(summary, "http_req_failed").get("value"),
        "hit_count": metric_values(summary, "analyze_cache_state{state:hit}").get("count", 0),
        "miss_count": metric_values(summary, "analyze_cache_state{state:miss}").get("count", 0),
        "unknown_count": metric_values(summary, "analyze_cache_state{state:unknown}").get("count", 0),
    }


def scenario_dedup(stub: StubControl, base_url: str, args, k6_path: str) -> dict:
    print("\n[3/3] 场景③ 并发去重正确性（singleflight 语义验证）")
    stub.reset()
    stub.configure(PROFILES["dedup"])
    deck_id = f"dedup{_run_stamp()}"
    code, summary = run_k6(
        REPO_ROOT / "perf" / "singleflight-dedup.k6.js",
        {"BASE_URL": base_url, "DECK_ID": deck_id, "VUS": str(args.dedup_vus)},
        k6_path=k6_path,
    )
    stats = stub.stats()
    states = {state: metric_values(summary, f"analyze_cache_state{{state:{state}}}").get("count", 0)
              for state in ("miss", "coalesce", "hit", "unknown")}
    iterations = metric_values(summary, "iterations").get("count", 0)
    # k6 闸门之外的第二道复核：迭代数齐、上游计数恰好一次编排
    upstream_ok = (
        stats.get("moxfield") == 1
        and stats.get("salt") == 1
        and iterations == args.dedup_vus
    )
    return {
        "exit_code": code and (0 if upstream_ok else 1),
        "iterations": iterations,
        "states": states,
        "upstream": {k: stats.get(k, 0) for k in ("moxfield", "salt", "edhrec", "getcards")},
        "upstream_ok": upstream_ok,
    }


def _run_stamp() -> str:
    return _dt.datetime.now().strftime("%H%M%S")


def _fmt(value, unit="", key=""):
    if value is None:
        return "n/a"
    if key == "error_rate":
        return f"{value:.2%}"
    if isinstance(value, float):
        return f"{value:.1f}{unit}"
    return f"{value}{unit}"


def report(name: str, result: dict, rss_peak_kb: int | None) -> None:
    print(f"\n===== {name} =====")
    for key, value in result.items():
        if key == "exit_code":
            continue
        print(f"  {key:16} {_fmt(value, key=key)}")
    if rss_peak_kb:
        print(f"  {'rss_peak':16} {rss_peak_kb / 1024:.1f}MB（被测服务进程内存峰值）")


def compare_baseline(baseline: dict, name: str, result: dict, tolerance: float) -> list[str]:
    """P95/RPS 回归闸门：P95 比基线慢超过 tolerance%，或 RPS 掉超过同比例即失败。"""
    problems = []
    reference = baseline.get(name)
    if not reference or result.get("p95_ms") is None and result.get("warm_p95_ms") is None:
        return problems
    current_p95 = result.get("p95_ms", result.get("warm_p95_ms"))
    if reference.get("p95_ms") and current_p95 > reference["p95_ms"] * (1 + tolerance):
        problems.append(f"{name}: P95 {current_p95:.0f}ms 比基线 {reference['p95_ms']:.0f}ms 回归超过 {tolerance:.0%}")
    if reference.get("rps") and result.get("rps") and result["rps"] < reference["rps"] * (1 - tolerance):
        problems.append(f"{name}: RPS {result['rps']:.1f} 比基线 {reference['rps']:.1f} 回归超过 {tolerance:.0%}")
    return problems


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--scenario", choices=["cold", "cache", "dedup", "all"], default="all")
    parser.add_argument("--fault", action="store_true", help="冷启动场景注入 salt 源超时（软降级画像）")
    parser.add_argument("--vus", type=int, default=10, help="冷启动/缓存场景并发数（默认 10）")
    parser.add_argument("--duration", default="30s", help="冷启动/缓存场景时长（默认 30s）")
    parser.add_argument("--dedup-vus", type=int, default=50, help="去重场景并发数（默认 50）")
    parser.add_argument("--update-baseline", action="store_true", help="用本次结果刷新 perf/baseline.json")
    parser.add_argument("--tolerance", type=float, default=0.25, help="基线回归容忍度（默认 0.25=25%%）")
    parser.add_argument("--k6", default="k6", help="k6 可执行文件路径")
    args = parser.parse_args()

    k6_path = resolve_k6(args.k6)
    if k6_path is None:
        print("未找到 k6。安装：winget install GrafanaLabs.k6（或 choco/scoop），或用 --k6 指定路径。")
        return 2

    if args.fault and args.scenario not in ("cold", "all"):
        print("--fault 只作用于冷启动场景")
        return 2

    binary = build_server_binary()
    if binary is None:
        print("需要可用 go 工具链现场编译被测服务")
        return 2

    # 一源一 stub 端口：Go 客户端对单 host 限 MaxConnsPerHost=10，六源共端口
    # 会先撞客户端连接池，测的是排队而非编排（计数/故障配置全局共享）
    servers, stub_bases = start_stub_pool(6)
    stub = StubControl(stub_bases[0])
    port = free_port()
    cache_dir = tempfile.mkdtemp(prefix="pl-perf-cache-")
    moxfield_base, salt_base, scryfall_base, spellbook_base, edhrec_base, getcards_base = stub_bases
    env_overrides = {
        "APP_ADDRESS": f"127.0.0.1:{port}",
        "POWERLEVEL_OPEN_BROWSER": "0",
        "MOXFIELD_API_URL": moxfield_base,
        "COMMANDERSALT_API_URL": salt_base,
        "SCRYFALL_API_URL": scryfall_base,
        "SPELLBOOK_API_URL": spellbook_base,
        "EDHREC_JSON_URL": edhrec_base,
        "GETCARDS_URL": getcards_base + "/getcards",
        "SCRYFALL_IMAGE_URL": scryfall_base + "/img",
        "CACHE_DIR": cache_dir,
        "SPELLBOOK_MIN_NAME_DELAY": "10ms",
    }
    if args.fault:
        # 收缩分源超时，让 fault 画像的 salt(3s) 稳定超时、其余源(≤400ms)不受影响
        env_overrides["PROVIDER_TIMEOUT"] = "1s"
        env_overrides["REQUEST_TIMEOUT"] = "10s"

    proc_env = os.environ.copy()
    proc_env.update(env_overrides)
    proc = subprocess.Popen([str(binary)], env=proc_env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    base_url = f"http://127.0.0.1:{port}"
    sampler = RSSSampler(proc.pid)
    sampler.start()

    baseline = {}
    if BASELINE_PATH.exists():
        baseline = json.loads(BASELINE_PATH.read_text(encoding="utf-8"))
    new_baseline = dict(baseline)
    failures: list[str] = []
    results: dict[str, dict] = {}

    try:
        wait_ready(base_url, timeout=60)
        print(f"被测服务 {base_url}（stub 池 {stub_bases[0]}…+5，CACHE_DIR={cache_dir}）")

        if args.scenario in ("cold", "all"):
            results["cold" + ("-fault" if args.fault else "")] = scenario_cold(stub, base_url, args, k6_path)
        if args.scenario in ("cache", "all"):
            results["cache"] = scenario_cache(stub, base_url, args, k6_path)
        if args.scenario in ("dedup", "all"):
            results["dedup"] = scenario_dedup(stub, base_url, args, k6_path)
    finally:
        rss_samples = sampler.stop()
        stop_process(proc)
        for stub_server in servers:
            stub_server.shutdown()
        import shutil as _shutil

        _shutil.rmtree(cache_dir, ignore_errors=True)

    rss_peak = max(rss_samples) if rss_samples else None
    stamp = _dt.datetime.now().isoformat(timespec="seconds")
    print("\n================ 压测报告 ================")
    print(f"时间 {stamp} | cold/cache VUS={args.vus} DURATION={args.duration} dedup VUS={args.dedup_vus} | 服务峰值内存 {_fmt(rss_peak and rss_peak / 1024, 'MB')}")
    for name, result in results.items():
        report(name, result, rss_peak)
        if result["exit_code"] != 0:
            failures.append(f"{name}: k6 阈值未通过（exit={result['exit_code']}）")
        if args.update_baseline:
            p95 = result.get("p95_ms", result.get("warm_p95_ms"))
            if p95 is not None:
                new_baseline[name] = {"p95_ms": round(p95, 1), "rps": round(result["rps"], 2) if result.get("rps") else None,
                                      "recorded_at": stamp}

    if not args.update_baseline:
        for name, result in results.items():
            failures.extend(compare_baseline(baseline, name, result, args.tolerance))

    RESULTS_DIR.mkdir(parents=True, exist_ok=True)
    (RESULTS_DIR / f"run-{stamp.replace(':', '')}.json").write_text(
        json.dumps({"scenarios": results, "rss_peak_kb": rss_peak, "args": vars(args)}, ensure_ascii=False, indent=2),
        encoding="utf-8")
    if args.update_baseline:
        BASELINE_PATH.write_text(json.dumps(new_baseline, ensure_ascii=False, indent=2), encoding="utf-8")
        print(f"\n基线已刷新 -> {BASELINE_PATH}")

    if failures:
        print("\n未通过项：")
        for item in failures:
            print(f"  ✗ {item}")
        return 1
    print("\n全部场景通过（k6 阈值 + 基线回归 + 上游计数复核）")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
