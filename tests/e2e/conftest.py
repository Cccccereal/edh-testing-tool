"""全栈 E2E 环境装配：本地 stub 上游 + 指向 stub 的真实 Go 服务 + Chromium。

与根 conftest 的 base_url（真实上游、契约黑盒）不同：这里六个上游环境变量
全部指向 tests/upstream_stub.py，整个旅程跑的是真实浏览器 + 真实 Go 编排 +
确定性假数据，不触达任何第三方，也不会被第三方限流弄花。

分层说明：既有 route 拦截型 UI 测试（test_ui_*.py）仍充当「UI 冒烟」层；
本目录的旅程 spec 是「数据正确」层——断言的是从 Go 编排到前端渲染的完整链路。
"""

import os
import pathlib
import shutil
import subprocess
import sys
import tempfile

import pytest

TESTS_DIR = pathlib.Path(__file__).resolve().parents[1]
if str(TESTS_DIR) not in sys.path:
    sys.path.insert(0, str(TESTS_DIR))

from helpers import build_server_binary, free_port, stop_process, wait_ready  # noqa: E402
from upstream_stub import start_stub  # noqa: E402


class StubControl:
    """对 stub 控制面的薄封装：查计数 / 清零 / 注入故障。"""

    def __init__(self, base_url: str):
        import requests

        self.base = base_url.rstrip("/")
        self.http = requests.Session()

    def stats(self) -> dict:
        return self.http.get(self.base + "/__stats", timeout=5).json()["counts"]

    def reset(self) -> None:
        self.http.post(self.base + "/__reset", timeout=5)

    def configure(self, behavior: dict) -> None:
        self.http.post(self.base + "/__configure", json=behavior, timeout=5)


@pytest.fixture(scope="session")
def stub():
    server, base = start_stub()
    yield {"base": base, "control": StubControl(base)}
    server.shutdown()


@pytest.fixture(scope="session")
def stubbed_server(stub):
    """编译当前源码并以六源全指向 stub 的环境变量启动被测服务。

    CACHE_DIR 指向独立临时目录：卡牌目录/getcards/图片代理的磁盘缓存
    不落进用户日常缓存，保证每次会话都是真正的冷启动。
    """
    binary = build_server_binary()
    if binary is None:
        pytest.fail("全栈 E2E 需要可用的 go 工具链现场编译被测服务")

    port = free_port()
    cache_dir = tempfile.mkdtemp(prefix="pl-e2e-cache-")
    stub_base = stub["base"]
    env = os.environ.copy()
    env.update({
        "APP_ADDRESS": f"127.0.0.1:{port}",
        "POWERLEVEL_OPEN_BROWSER": "0",
        "MOXFIELD_API_URL": stub_base,
        "COMMANDERSALT_API_URL": stub_base,
        "SCRYFALL_API_URL": stub_base,
        "SPELLBOOK_API_URL": stub_base,
        "EDHREC_JSON_URL": stub_base,
        "GETCARDS_URL": stub_base + "/getcards",
        "SCRYFALL_IMAGE_URL": stub_base + "/img",
        "CACHE_DIR": cache_dir,
        # stub 不会限流，把 Spellbook 的按名限速间隔降到毫秒级，
        # 冷启动分析不再被 12×1.5s 的节奏拖住（生产行为见 .env.example）
        "SPELLBOOK_MIN_NAME_DELAY": "10ms",
    })
    proc = subprocess.Popen(
        [str(binary)],
        cwd=TESTS_DIR.parent,
        env=env,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
    )
    try:
        wait_ready(f"http://127.0.0.1:{port}", timeout=60)
        yield {"base_url": f"http://127.0.0.1:{port}", "stub": stub}
    finally:
        stop_process(proc)
        shutil.rmtree(cache_dir, ignore_errors=True)


@pytest.fixture
def page(stubbed_server):
    """每个用例独立 launch + 上下文。

    不能把 sync_playwright 会话做成 session 级：合跑时它的上下文横跨
    全程，后面 tests/test_ui_*.py 各自再开 sync_playwright 会报
    "Sync API inside the asyncio loop"。每个用例开关一次，互不沾染。
    """
    pytest.importorskip("playwright")
    from playwright.sync_api import sync_playwright

    with sync_playwright() as manager:
        browser = manager.chromium.launch(headless=True)
        context = browser.new_context(base_url=stubbed_server["base_url"])
        opened = context.new_page()
        opened.set_default_timeout(15_000)
        # 记录谁改了 #deck-drawer 的 hidden（排障用；读取 window.__drawer_mutations）
        opened.add_init_script(
            """
            window.__drawer_mutations = [];
            const report = () => {
              const el = document.querySelector('#deck-drawer');
              if (el) window.__drawer_mutations.push({hidden: el.hidden, t: Math.round(performance.now()), stack: (new Error().stack || '').split('\\n').slice(1, 4).join(' <- ')});
            };
            document.addEventListener('DOMContentLoaded', () => {
              report();
              new MutationObserver(report).observe(document.querySelector('#deck-drawer'), {attributes: true, attributeFilter: ['hidden']});
            });
            """
        )
        yield opened
        context.close()
        browser.close()


@pytest.fixture(autouse=True)
def _report_page_state_on_failure(request, page):
    """失败时落一张截图 + 关键 DOM 状态，便于旅程调试。"""
    request.node._e2e_page = page
    yield


@pytest.hookimpl(hookwrapper=True)
def pytest_runtest_makereport(item, call):
    outcome = yield
    report = outcome.get_result()
    setattr(item, f"rep_{report.when}", report)
    if report.when == "call" and report.failed:
        page = getattr(item, "_e2e_page", None)
        if page:
            import pathlib as _pathlib

            out_dir = TESTS_DIR.parent / ".run" / "e2e-failures"
            out_dir.mkdir(parents=True, exist_ok=True)
            try:
                page.screenshot(path=str(out_dir / f"{item.name}.png"), full_page=False)
            except Exception:
                pass
            try:
                mutations = page.evaluate("window.__drawer_mutations || []")
                if mutations:
                    print(f"\n[e2e-debug] {item.name} #deck-drawer hidden 突变记录:")
                    for entry in mutations:
                        print(f"  t={entry['t']}ms hidden={entry['hidden']} <- {entry['stack'][:160]}")
            except Exception:
                pass
            for line in str(report.longrepr).splitlines():
                if "AssertionError" in line or "TimeoutError" in line:
                    print(f"\n[e2e-debug] {item.name}: {line.strip()[:200]}")
