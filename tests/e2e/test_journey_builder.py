"""旅程⑤：组牌器（全栈）——主将补全 → 开始 → 三选一 → 出地/常见单卡 → 导出。

组牌器的数据链路：commander-autocomplete（stub Scryfall）→
resolve-commanders / build-suggest（真实推荐管线 + stub EDHREC）→
build-lands / build-staples。全程不触第三方。
"""

import pytest
from playwright.sync_api import expect

from journey_lib import open_home

pytestmark = pytest.mark.ui


def test_builder_journey(page, stubbed_server):
    open_home(page)
    page.click("#build-entry-button")
    builder = page.locator("#builder")
    expect(builder).to_be_visible()

    # 主将输入 → 自动补全（stub Scryfall autocomplete）
    page.fill("#builder-commander", "Sram")
    suggestions = page.locator("#builder-commander-suggestions li")
    expect(suggestions.first).to_be_visible()
    suggestions.first.click()
    expect(page.locator("#builder-commander")).to_have_value("Sram, Senior Edificer")

    # 开始 → build-suggest 返回三选一候选
    with page.expect_response(lambda r: "/api/v1/build-suggest" in r.url):
        page.click("#builder-start")
    workflow = page.locator("#builder-workflow")
    expect(workflow).to_be_visible()
    expect(page.locator("#builder-candidates .builder-candidate")).to_have_count(3)

    # 三选一加入一张
    page.locator(".builder-candidate[data-candidate='0']").click()

    # 一键出地：类别按钮 → 类别内地牌加入草稿
    page.click("#builder-lands")
    lands_panel = page.locator("#builder-lands-panel")
    expect(lands_panel).to_be_visible()
    page.locator("[data-land-category='fetch']").click()
    land_card = page.locator("#builder-lands-result .builder-land-card").first
    expect(land_card).to_be_visible()
    land_card.click()

    # 常见单卡：同样两步
    page.click("#builder-staples")
    staples_panel = page.locator("#builder-staples-panel")
    expect(staples_panel).to_be_visible()
    page.locator("[data-staple-category='ramp']").click()
    staple_card = page.locator("#builder-staples-result .builder-land-card").first
    expect(staple_card).to_be_visible()
    staple_card.click()

    # 侧栏构筑指标随草稿刷新
    expect(page.locator("#builder-sidebar").locator("*").first).to_be_visible()

    # 用快速加基本地把草稿填满 100 张——#builder-complete 只在组满时出现，
    # 「导出牌表」按钮住在完成区里（UI 没有别的导出入口）
    page.evaluate(
        """() => {
          const button = document.querySelector("button[data-basic='Plains']");
          for (let i = 0; i < 120; i++) button.click();
        }"""
    )
    expect(page.locator("#builder-complete")).to_be_visible()

    # 导出草稿牌表
    with page.expect_download() as download_info:
        page.click("#builder-export")
    assert download_info.value.suggested_filename == "decklist.txt"


def test_builder_random_commander(page, stubbed_server):
    """随机主将从 EDHREC 排行（stub year.json）抽取。"""
    open_home(page)
    page.click("#build-entry-button")
    with page.expect_response(lambda r: "/api/v1/random-commander" in r.url):
        page.click("#builder-random")
    expect(page.locator("#builder-commander")).not_to_be_empty()
