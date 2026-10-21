"""旅程④：编辑器加减卡 → 即时测算对比 → 导出（全栈）。

「单卡替换预览」弹窗已下线，当前等价旅程是牌表抽屉里 +/− 改牌、
「重新测算」跑一遍完整分析流水线并渲染基线 vs 当前的 delta 表，
最后导出编辑后的牌表文本。第二次分析走真实 Go 编排（stub 上游），
X-Cache 应为 miss——编辑后的牌表是新的缓存 key。
"""

import pytest
from playwright.sync_api import expect

from journey_lib import CANONICAL_URL, open_home, submit_analyze, wait_for_results

pytestmark = pytest.mark.ui


def test_editor_recompare_and_export_journey(page, stubbed_server):
    open_home(page)
    _, payload = submit_analyze(page, url=CANONICAL_URL)
    wait_for_results(page)

    # 分析完成后右侧牌表抽屉已自动展开（renderDeckCards 有卡即显示），编辑器就绪。
    # 注：#deck-drawer 是零宽定位容器，可见性要断 .deck-drawer-panel。
    drawer_list = page.locator("#deck-card-list")
    expect(page.locator(".deck-drawer-panel")).to_be_visible()
    expect(drawer_list).to_be_visible()
    assert drawer_list.locator("[data-card-add]").count() > 0

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

    # 导出编辑后的牌表
    with page.expect_download() as download_info:
        page.click("#editor-export")
    download = download_info.value
    assert download.suggested_filename == "decklist.txt"
    text = download.path() and _read_download(download)
    assert "Sol Ring" in text
    assert text.count("Sol Ring") == 1 or "2 Sol Ring" in text


def _read_download(download) -> str:
    path = download.path()
    with open(path, encoding="utf-8") as handle:
        return handle.read()
