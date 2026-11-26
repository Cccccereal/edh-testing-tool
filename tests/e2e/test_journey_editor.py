"""旅程④：编辑器加减卡 → 即时测算对比 → 导出（全栈）。

「单卡替换预览」弹窗已下线，当前等价旅程是牌表抽屉里 +/− 改牌、
「重新测算」跑一遍完整分析流水线并渲染基线 vs 当前的 delta 表，
最后导出编辑后的牌表文本。第二次分析走真实 Go 编排（stub 上游），
X-Cache 应为 miss——编辑后的牌表是新的缓存 key。
"""

import pytest
from playwright.sync_api import expect

from journey_lib import CANONICAL_URL, open_home, read_clipboard_text, submit_analyze, wait_for_results

pytestmark = pytest.mark.ui


def test_editor_recompare_and_export_journey(page, stubbed_server):
    open_home(page)
    _, payload = submit_analyze(page, url=CANONICAL_URL)
    wait_for_results(page)

    # 分析完成后牌表抽屉保持收起——结果页先让人看评分；从导航拉出来后编辑器就绪。
    # 注：#deck-drawer 是零宽定位容器，可见性要断 .deck-drawer-panel。
    expect(page.locator("#deck-drawer")).to_be_hidden()
    page.click("[data-deck-drawer-open]")
    expect(page.locator(".deck-drawer-panel")).to_be_visible()
    drawer_list = page.locator("#deck-card-list")
    expect(drawer_list).to_be_visible()
    assert drawer_list.locator("[data-card-add]").count() > 0

    # 文字行视图（默认）：按类型分组、每行一张牌；切到图像视图是卡图网格
    assert drawer_list.locator(".deck-row").count() > 0
    page.click("[data-deck-view='grid']")
    expect(drawer_list.locator(".card-grid .mtg-card").first).to_be_visible()
    expect(drawer_list.locator(".deck-row")).to_have_count(0)
    page.click("[data-deck-view='list']")
    expect(drawer_list.locator(".deck-row").first).to_be_visible()

    # 给 Sol Ring +1 → 编辑器进入脏状态，撤销可用
    page.click("[data-card-add='Sol Ring']")
    expect(page.locator("#editor-dirty-note")).to_be_visible()
    expect(page.locator("#editor-undo")).to_be_enabled()

    # 重新测算：编辑后的牌表是新的缓存 key，应真实回源（X-Cache=miss）
    with page.expect_response(lambda r: "/api/v1/analyze" in r.url) as response_info:
        page.click("#editor-compare-run")
    response = response_info.value
    assert response.status == 200
    assert response.headers["x-cache"] == "miss"
    compared = response.json()
    assert compared["status"] == "success"
    assert compared["deck"]["card_count"] == payload["deck"]["card_count"] + 1

    # 对比表渲染：基线 vs 当前，每行带 delta 单元格
    result = page.locator("#editor-compare-result")
    expect(result).to_be_visible()
    expect(result.locator(".swap-delta").first).to_be_visible()

    # 导出编辑后的牌表：剪贴板优先——「复制 → 粘贴进 Moxfield」是主工作流，桌面
    # 客户端（Tauri WebView）里 <a download> 又不可靠；复制失败才退回文件下载
    # （见下一条旅程）。上面给 Sol Ring +1，导出的牌表里它是 2 张、且只占一行
    page.click("#editor-export")
    expect(page.locator("#editor-export")).to_have_text("已复制")
    text = read_clipboard_text(page)
    assert text.startswith("Commander\n")
    assert [line for line in text.splitlines() if "Sol Ring" in line] == ["2 Sol Ring"]


def test_editor_export_downloads_when_clipboard_is_unavailable(page, stubbed_server):
    """剪贴板不可用时（安卓 WebView 的常态）导出退回文件下载。

    两条复制路径都掐掉——navigator.clipboard 与 document.execCommand('copy')——
    否则剪贴板可用时根本不会有下载事件。
    """
    # 注入脚本要写成裸语句：add_init_script 只对脚本求值，写成箭头函数不会被调用
    page.add_init_script(
        """
        Object.defineProperty(navigator, 'clipboard', { value: undefined, configurable: true });
        document.execCommand = () => false;
        """
    )
    open_home(page)
    submit_analyze(page, url=CANONICAL_URL)
    wait_for_results(page)
    page.click("[data-deck-drawer-open]")
    expect(page.locator(".deck-drawer-panel")).to_be_visible()

    with page.expect_download() as download_info:
        page.click("#editor-export")
    # 「已下载」只显示约 1.6 秒，先断它再读文件
    expect(page.locator("#editor-export")).to_have_text("已下载")
    download = download_info.value
    assert download.suggested_filename == "decklist.txt"
    text = _read_download(download)
    assert text.startswith("Commander\n")
    assert "Sol Ring" in text


def _read_download(download) -> str:
    path = download.path()
    with open(path, encoding="utf-8") as handle:
        return handle.read()
