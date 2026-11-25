#!/usr/bin/env python3
"""金标准语料评测：把「主管觉得不准」变成秩相关系数。

用法（零第三方依赖，Python 3.8+）：

  python eval/evaluate.py --server http://localhost:18781
      对语料库全部牌组采集评分并立即评测（牌表缺失但有 source_url 时，
      会经服务端解析 Moxfield 并把牌表快照存进 corpus/decks/<id>.txt）。

  python eval/evaluate.py --scores eval/results/latest.json
      离线评测既有快照（不打服务端）。

  python eval/evaluate.py --server http://localhost:18781 --collect-only
      只刷新评分快照，不评测。

  python eval/evaluate.py --scores eval/results/latest.json --metric commandersalt.salt
      换一个评分口径评测（默认 health，即工具自有健康分），
      可用于回答「我们的分比 CommanderSalt 更贴主管吗」。

  python eval/evaluate.py --scores eval/results/latest.json --save-baseline
      把本次结果存为基线 eval/baseline.json（随代码提交，供回归对比）。

  python eval/evaluate.py --worksheet
      打印打乱的标注工作表（只给名称与主将，不给分数，避免锚定），
      主管排好序后把 1..N 抄回 corpus/decks.csv 的 supervisor_rank 列。

  python eval/evaluate.py --selftest
      统计函数自检。

约定：supervisor_rank 填 1..N，**1 = 最强**。留空的牌组不参与统计。
"""
import argparse
import csv
import json
import os
import random
import subprocess
import sys
import time
import urllib.error
import urllib.request
from datetime import datetime
from pathlib import Path

EVAL_DIR = Path(__file__).resolve().parent
CORPUS_DIR = EVAL_DIR / "corpus"
DECKS_CSV = CORPUS_DIR / "decks.csv"
DECKS_DIR = CORPUS_DIR / "decks"
RESULTS_DIR = EVAL_DIR / "results"
LATEST = RESULTS_DIR / "latest.json"
BASELINE = EVAL_DIR / "baseline.json"

CSV_FIELDS = ["deck_id", "name", "commander", "source_url", "supervisor_rank", "note"]

# 快照里采集的评分候选口径。评测时用 --metric 选一个。
METRIC_KEYS = ["health", "commandersalt.salt", "commandersalt.power_level",
               "edhpowerlevel.power_level", "edhpowerlevel.bracket"]


# ---------------------------------------------------------------- 统计（带并列秩修正）

def rankdata(values):
    """平均秩（1 起）。并列取平均，Spearman 标准处理。"""
    order = sorted(range(len(values)), key=lambda i: values[i])
    ranks = [0.0] * len(values)
    i = 0
    while i < len(order):
        j = i
        while j + 1 < len(order) and values[order[j + 1]] == values[order[i]]:
            j += 1
        avg = (i + j) / 2 + 1
        for k in range(i, j + 1):
            ranks[order[k]] = avg
        i = j + 1
    return ranks


def _pearson(x, y):
    n = len(x)
    mx, my = sum(x) / n, sum(y) / n
    dx = [a - mx for a in x]
    dy = [b - my for b in y]
    num = sum(a * b for a, b in zip(dx, dy))
    den = (sum(a * a for a in dx) * sum(b * b for b in dy)) ** 0.5
    return num / den if den else 0.0


def spearman(x, y):
    return _pearson(rankdata(x), rankdata(y))


def kendall_tau_b(x, y):
    n = len(x)
    conc = disc = tie_x = tie_y = 0
    for i in range(n):
        for j in range(i + 1, n):
            sx = (x[i] > x[j]) - (x[i] < x[j])
            sy = (y[i] > y[j]) - (y[i] < y[j])
            if sx and sy:
                if sx == sy:
                    conc += 1
                else:
                    disc += 1
            elif sx:
                tie_x += 1
            elif sy:
                tie_y += 1
    den = ((conc + disc + tie_x) * (conc + disc + tie_y)) ** 0.5
    return (conc - disc) / den if den else 0.0


def selftest():
    cases = [
        (([1, 2, 3, 4, 5], [1, 2, 3, 4, 5]), 1.0),
        (([1, 2, 3, 4, 5], [5, 4, 3, 2, 1]), -1.0),
        (([1, 2, 3, 4, 5], [1, 3, 2, 5, 4]), 0.8),
        # 并列：平均秩 Pearson，scipy.stats.spearmanr([1,2,2,4],[1,2,3,4]) = 3/√10
        (([1, 2, 2, 4], [1, 2, 3, 4]), 0.9486832980505138),
    ]
    for (x, y), want in cases:
        got = spearman(x, y)
        assert abs(got - want) < 1e-12, f"spearman{x},{y} = {got}, want {want}"
    # Kendall tau-b（scipy.stats.kendalltau 同值）
    assert abs(kendall_tau_b([1, 2, 3, 4, 5], [3, 4, 1, 2, 5]) - 0.2) < 1e-12
    assert abs(kendall_tau_b([1, 2, 3, 4], [1, 1, 2, 2]) - 0.8164965809277261) < 1e-12
    print("selftest ok")


# ---------------------------------------------------------------- 语料库

def load_corpus():
    if not DECKS_CSV.exists():
        sys.exit(f"找不到 {DECKS_CSV}")
    rows = []
    with open(DECKS_CSV, newline="", encoding="utf-8-sig") as f:
        for row in csv.DictReader(f):
            row = {k: (v or "").strip() for k, v in row.items() if k}
            if row.get("deck_id"):
                rows.append(row)
    if not rows:
        sys.exit("decks.csv 里没有牌组条目")
    return rows


def deck_path(deck_id):
    return DECKS_DIR / f"{deck_id}.txt"


def parse_ranks(rows):
    """返回 {deck_id: int_rank}，只收纯数字且 ≥1 的。"""
    ranks = {}
    for row in rows:
        raw = row.get("supervisor_rank", "")
        try:
            value = int(raw)
        except (TypeError, ValueError):
            continue
        if value >= 1:
            ranks[row["deck_id"]] = value
    return ranks


# ---------------------------------------------------------------- 采集

def analyze(server, payload, timeout):
    req = urllib.request.Request(
        server.rstrip("/") + "/api/v1/analyze",
        data=json.dumps(payload).encode("utf-8"),
        headers={"Content-Type": "application/json"},
        method="POST",
    )
    with urllib.request.urlopen(req, timeout=timeout) as resp:
        return json.loads(resp.read().decode("utf-8"))


def extract_candidates(body):
    candidates = {}
    health = body.get("health") or {}
    if isinstance(health.get("score"), (int, float)):
        candidates["health"] = round(float(health["score"]), 2)
    results = body.get("results") or {}
    cs = (results.get("commandersalt") or {}).get("metrics") or {}
    ep = (results.get("edhpowerlevel") or {}).get("metrics") or {}
    for key, source in [("commandersalt.salt", cs), ("commandersalt.power_level", cs),
                        ("edhpowerlevel.power_level", ep), ("edhpowerlevel.bracket", ep)]:
        value = source.get(key.split(".", 1)[1])
        if isinstance(value, (int, float)):
            candidates[key] = round(float(value), 2)
    status = {name: (results.get(name) or {}).get("status", "missing")
              for name in ("commandersalt", "edhpowerlevel")}
    return candidates, status


def collect(args, rows):
    server = args.server
    deck_rows = []
    failed = []
    for row in rows:
        did = row["deck_id"]
        path = deck_path(did)
        payload = None
        if path.exists():
            payload = {"decklist": path.read_text(encoding="utf-8-sig")}
        elif row.get("source_url"):
            payload = {"url": row["source_url"]}
        else:
            failed.append((did, "既无 corpus/decks/<id>.txt 也无 source_url"))
            continue
        label = row.get("name") or did
        print(f"[采集] {did}（{label}）...", flush=True)
        try:
            body = analyze(server, payload, args.timeout)
        except (urllib.error.URLError, TimeoutError, OSError) as exc:
            failed.append((did, f"请求失败：{exc}"))
            continue
        candidates, status = extract_candidates(body)
        # URL 首采成功就把牌表快照落地，之后回归不再依赖外链是否还活着。
        if "decklist" in payload and body.get("canonical_decklist"):
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text(body["canonical_decklist"], encoding="utf-8")
            print(f"        牌表快照已存 {path.relative_to(EVAL_DIR)}")
        deck_rows.append({
            "deck_id": did,
            "name": label,
            "commander": row.get("commander", ""),
            "card_count": (body.get("deck") or {}).get("card_count"),
            "commanders": (body.get("deck") or {}).get("commanders") or [],
            "candidates": candidates,
            "provider_status": status,
            "error": None,
        })
    snapshot = {
        "generated_at": datetime.now().isoformat(timespec="seconds"),
        "server": server,
        "decks": deck_rows,
    }
    RESULTS_DIR.mkdir(parents=True, exist_ok=True)
    stamp = datetime.now().strftime("%Y%m%d-%H%M%S")
    out = Path(args.out) if args.out else RESULTS_DIR / f"scores-{stamp}.json"
    out.write_text(json.dumps(snapshot, ensure_ascii=False, indent=2), encoding="utf-8")
    LATEST.write_text(json.dumps(snapshot, ensure_ascii=False, indent=2), encoding="utf-8")
    print(f"\n快照已写入 {out.relative_to(EVAL_DIR)}（同时更新 results/latest.json）")
    if failed:
        print("\n以下牌组采集失败：")
        for did, reason in failed:
            print(f"  - {did}: {reason}")
    return snapshot, bool(failed)


# ---------------------------------------------------------------- 评测

def evaluate_with_ranks(snapshot, metric, ranks):
    """核心评测：metric 口径的工具分 vs 主管排序（1 = 最强）。"""
    entries = []
    for deck in snapshot["decks"]:
        rank = ranks.get(deck["deck_id"])
        if rank is None:
            continue
        score = (deck.get("candidates") or {}).get(metric)
        if score is None:
            print(f"[跳过] {deck['deck_id']}：有主管排序但没有 {metric} 分数"
                  f"（provider_status={deck.get('provider_status')}）")
            continue
        entries.append({"deck_id": deck["deck_id"], "name": deck.get("name") or deck["deck_id"],
                        "rank": rank, "score": score})
    if len(entries) < 3:
        print(f"\n参与统计的牌组不足 3 套（当前 {len(entries)} 套），无法给出秩相关。")
        print("下一步：跑 --worksheet 给主管排序，把 1..N 填回 corpus/decks.csv 的 supervisor_rank。")
        return None, entries

    # 强度序列：rank 1 = 最强 → strength 最大。
    n = len(entries)
    strength = [n + 1 - e["rank"] for e in entries]
    scores = [e["score"] for e in entries]
    rho = spearman(strength, scores)
    tau = kendall_tau_b(strength, scores)

    print(f"\n=== 评测：{metric} vs 主管排序（{n} 套已标注）===")
    print(f"{'主管序':>4}  {'牌组ID':<24} {'名称':<20} {'工具分':>8}")
    for e in sorted(entries, key=lambda e: e["rank"]):
        print(f"{e['rank']:>5}  {e['deck_id']:<24} {str(e['name'])[:20]:<20} {e['score']:>8.2f}")
    print(f"\nSpearman ρ = {rho:+.3f}    Kendall τ-b = {tau:+.3f}")
    verdict = "对齐很好" if rho >= 0.85 else ("基本对齐" if rho >= 0.7 else "分歧明显，先看下面的不一致对")
    print(f"口径参考：≥0.85 {verdict.split('，')[0] if '，' in verdict else verdict}；"
          f"0.7-0.85 可接受；<0.7 说明评分口径与主管直觉系统性偏离")
    print(f"结论：{verdict}")

    # 不一致对：主管认为 A 强于 B，但工具给 A 更低分；按分差×序差排序。
    pairs = []
    for i in range(n):
        for j in range(i + 1, n):
            a, b = entries[i], entries[j]
            if a["rank"] == b["rank"]:
                continue
            stronger, weaker = (a, b) if a["rank"] < b["rank"] else (b, a)
            gap = weaker["score"] - stronger["score"]
            if gap > 0:  # 工具把弱的打分更高 → 不一致
                pairs.append((stronger, weaker, gap))
    if pairs:
        pairs.sort(key=lambda p: p[2], reverse=True)
        print(f"\n不一致对（共 {len(pairs)} / {n * (n - 1) // 2} 对，按分差降序，最多列 8 条）：")
        for stronger, weaker, gap in pairs[:8]:
            print(f"  主管认为「{stronger['name']}」>「{weaker['name']}」"
                  f"（第 {stronger['rank']} vs {weaker['rank']}），"
                  f"但工具分 {stronger['score']:.2f} < {weaker['score']:.2f}（差 {gap:.2f}）")
    return rho, entries


# ---------------------------------------------------------------- 基线

def git_commit():
    try:
        return subprocess.run(["git", "rev-parse", "--short", "HEAD"],
                              capture_output=True, text=True, cwd=EVAL_DIR,
                              timeout=10).stdout.strip() or None
    except Exception:
        return None


def save_baseline(metric, rho, tau, entries):
    baseline = {
        "generated_at": datetime.now().isoformat(timespec="seconds"),
        "git_commit": git_commit(),
        "metric": metric,
        "rho": rho,
        "kendall": tau,
        "labeled": [e["deck_id"] for e in sorted(entries, key=lambda e: e["rank"])],
        "scores": {e["deck_id"]: e["score"] for e in entries},
    }
    BASELINE.write_text(json.dumps(baseline, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    print(f"\n基线已保存 {BASELINE.relative_to(EVAL_DIR)}"
          f"（commit {baseline['git_commit'] or '未知'}，ρ={rho:+.3f}）")
    print("之后每次调参跑同口径评测即可对比；ρ 显著回退说明改动伤了与主管直觉的对齐。")


def compare_baseline(rho, metric, entries, fail_drop):
    if not BASELINE.exists():
        print("\n（无基线。调参前先 --save-baseline，之后就能看出每次改动的 ρ 变化。）")
        return 0
    baseline = json.loads(BASELINE.read_text(encoding="utf-8"))
    if baseline.get("metric") != metric:
        print(f"\n[提示] 基线口径是 {baseline['metric']}，本次是 {metric}，ρ 不可直接对比。")
        return 0
    delta = None if rho is None else rho - baseline.get("rho", rho)
    print(f"\n=== 基线对比（{baseline.get('git_commit') or '未知版本'}，"
          f"{baseline.get('generated_at', '?')}）===")
    print(f"ρ：基线 {baseline.get('rho', 0):+.3f} → 本次 {rho:+.3f}"
          + (f"（{'+' if delta >= 0 else ''}{delta:.3f}）" if delta is not None else ""))
    moved = []
    base_scores = baseline.get("scores", {})
    for e in entries:
        old = base_scores.get(e["deck_id"])
        if old is not None and abs(old - e["score"]) >= 0.01:
            moved.append((e["deck_id"], old, e["score"]))
    if moved:
        print("以下牌组的分数相对基线发生了变化：")
        for deck_id, old, new in sorted(moved, key=lambda m: abs(m[2] - m[1]), reverse=True):
            mark = "↑" if new > old else "↓"
            print(f"  {deck_id}: {old:.2f} → {new:.2f} {mark}")
    else:
        print("各牌组分数与基线一致（改动未影响已标注牌组的评分）。")
    if delta is not None and fail_drop > 0 and delta < -fail_drop:
        print(f"\n[回归] ρ 下降 {abs(delta):.3f} 超过容许值 {fail_drop}——本次改动请先回看。")
        return 1
    return 0


# ---------------------------------------------------------------- 工作表

def worksheet(rows):
    entries = [r for r in rows if r["deck_id"] != "example-pipeline"]
    if not entries:
        if rows:
            sys.exit("语料库目前只有 example-pipeline 示例条目：往 corpus/decks.csv 加真实牌组后再生成工作表")
        sys.exit("语料库为空：先往 corpus/decks.csv 加牌组（配 source_url 或 decks/<id>.txt）")
    shuffled = entries[:]
    random.shuffle(shuffled)
    print("标注工作表（顺序已打乱；请主管按强度从强到弱编 1..N，1 = 最强，")
    print("填入 corpus/decks.csv 的 supervisor_rank 列。故意不显示任何分数，避免锚定）：\n")
    for r in entries:
        name = r.get("name") or r["deck_id"]
        commander = r.get("commander") or ""
        tail = f" — 主将 {commander}" if commander else ""
        print(f"（序号 ____）  {name}{tail}   [deck_id: {r['deck_id']}]")


# ---------------------------------------------------------------- 主流程

def main():
    if os.name == "nt":
        try:
            sys.stdout.reconfigure(encoding="utf-8", errors="replace")
        except Exception:
            pass
    parser = argparse.ArgumentParser(description="金标准语料评测：秩相关 + 基线回归")
    parser.add_argument("--server", help="服务端地址，例如 http://localhost:18781（采集模式）")
    parser.add_argument("--scores", help="离线评测：既有快照路径（默认自动用 results/latest.json）")
    parser.add_argument("--out", help="采集快照输出路径（默认 results/scores-<时间戳>.json）")
    parser.add_argument("--collect-only", action="store_true", help="只采集，不评测")
    parser.add_argument("--metric", default="health",
                        help=f"评测口径（默认 health），可选：{', '.join(METRIC_KEYS)}")
    parser.add_argument("--save-baseline", action="store_true", help="评测后把结果存为基线")
    parser.add_argument("--worksheet", action="store_true", help="打印主管排序工作表")
    parser.add_argument("--fail-on-drop", type=float, default=0.0, metavar="X",
                        help="与基线相比 ρ 回退超过 X 则退出码 1（CI 回归门禁用）")
    parser.add_argument("--timeout", type=int, default=180, help="单副牌分析超时秒数（默认 180）")
    parser.add_argument("--selftest", action="store_true", help="统计函数自检")
    args = parser.parse_args()

    if args.selftest:
        selftest()
        return
    if args.worksheet:
        worksheet(load_corpus())
        return

    rows = load_corpus()
    if args.server and args.scores:
        sys.exit("--server 与 --scores 二选一")
    if args.server:
        snapshot, had_failures = collect(args, rows)
        if args.collect_only:
            return
    else:
        path = Path(args.scores) if args.scores else (LATEST if LATEST.exists() else None)
        if path is None or not path.exists():
            sys.exit("没有快照可用：先 --server 采集一次，或 --scores 指定快照路径")
        snapshot = json.loads(path.read_text(encoding="utf-8"))
        print(f"（离线评测快照 {path}，采集于 {snapshot.get('generated_at', '?')}）")

    ranks = parse_ranks(rows)
    unlabeled = len(rows) - len(ranks)
    if unlabeled:
        print(f"（{unlabeled} 套牌组还没有主管排序，不参与统计）")
    rho, entries = evaluate_with_ranks(snapshot, args.metric, ranks)

    exit_code = 0
    if rho is not None:
        tau = kendall_tau_b([len(entries) + 1 - e["rank"] for e in entries],
                            [e["score"] for e in entries])
        if args.save_baseline:
            save_baseline(args.metric, rho, tau, entries)
        exit_code = compare_baseline(rho, args.metric, entries, args.fail_on_drop)
    sys.exit(exit_code)


if __name__ == "__main__":
    main()
