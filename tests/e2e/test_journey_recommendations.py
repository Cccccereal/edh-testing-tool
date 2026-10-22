"""旅程③：构筑推荐双视图（全栈）——缺口词条 Tab + 常见搭配 Tab。

缺口视图按构筑指标分桶（含 Scryfall 语义兜底的卡，带来源徽章），类别视图
保留 EDHREC 原生分组。断言两个 Tab 的分组数/卡数与 API 口径一致、缺口桶
默认收起可展开、词条检索徽章可见、「直接加入构筑」把卡写进编辑器。
"""

import re

import pytest
from playwright.sync_api import expect

from journey_lib import CANONICAL_URL, open_home, submit_analyze, wait_for_results

pytestmark = pytest.mark.ui


def test_gap_recommendation_journey(page, stubbed_server):
    open_home(page)
    _, payload = submit_analyze(page, url=CANONICAL_URL)
    wait_for_results(page)

    gap_recs = payload.get("gap_recommendations") or []
    recommendations = payload["recommendations"]
    assert gap_recs, "stub 牌组应有缺口词条推荐"
    assert len(recommendations) >= 2, "stub EDHREC 页应至少留下两个类别分组"

    # 分析完成后右侧牌表抽屉会自动展开遮住主页面，先收起再操作推荐区
    page.click("#deck-drawer-close")
    expect(page.locator("#deck-drawer")).to_be_hidden()

    section = page.locator("#recommendation-section")
    expect(section).to_be_visible()
    expect(page.locator("#recommendation-keywords")).to_contain_text("常见主题")

    # 双 Tab 可见，默认落在缺口词条视图
    tabs = page.locator("#recommendation-tabs")
    expect(tabs).to_be_visible()
    expect(tabs.locator(".recommendation-tab.is-active")).to_contain_text("缺口词条")

    gap_groups = page.locator(".recommendation-group.gap-recommendation-group")
    expect(gap_groups).to_have_count(len(gap_recs))
    # 每个缺口桶的标题与"还缺 N"来自 API；桶内卡数（含收起组，DOM 计数）一致
    for i, metric in enumerate(gap_recs):
        heading = gap_groups.nth(i).locator(".recommendation-group-heading")
        expect(heading).to_contain_text(metric["label"])
        expect(heading).to_contain_text(f"还缺 {metric['gap']}")
    expect(page.locator(".gap-recommendation-group .recommendation-card")).to_have_count(
        sum(len(metric.get("cards", [])) for metric in gap_recs)
    )

    # 词条检索兜底：API 里带 source=scryfall 的卡，前端应有对应徽章与热度排名
    scryfall_names = [
        card["card"]["name"]
        for metric in gap_recs
        for card in metric.get("cards", [])
        if card.get("source") == "scryfall"
    ]
    assert scryfall_names, "stub 语义查询应至少兜底一张卡（如检索/牌差词条）"
    badges = page.locator(".gap-recommendation-group .recommendation-source")
    expect(badges).to_have_count(len(scryfall_names))
    expect(badges.first).to_have_text("词条检索")
    scryfall_card = page.locator(".gap-recommendation-group .recommendation-card.is-scryfall").first
    expect(scryfall_card.locator(".recommendation-meta")).to_contain_text("热度排名")

    # 缺口桶默认收起 → 展开第一个桶 → 推荐卡可见
    first = gap_groups.first
    collapsed = re.compile(r".*is-collapsed.*")
    expect(first).to_have_class(collapsed)
    first.locator("[data-recommendation-toggle]").first.click()
    expect(first).not_to_have_class(collapsed)
    expect(first.locator(".recommendation-card").first).to_be_visible()

    # 切到常见搭配视图：分组数与 API 一致（不要求补缺口的卡也在）
    tabs.locator(".recommendation-tab", has_text="常见搭配").click()
    expect(tabs.locator(".recommendation-tab.is-active")).to_contain_text("常见搭配")
    category_groups = page.locator("#recommendation-list .recommendation-group:not(.gap-recommendation-group)")
    expect(category_groups).to_have_count(len(recommendations))
    expect(page.locator(".recommendation-card:not(.is-scryfall)")).to_have_count(
        sum(len(group.get("cards", [])) for group in recommendations)
    )

    # 「直接加入构筑」→ 按钮翻转 + 编辑器进入脏状态
    category_groups.first.locator("[data-recommendation-toggle]").first.click()
    add_button = category_groups.first.locator(".recommend-add").first
    added_name = add_button.get_attribute("data-add-name")
    assert added_name, "推荐卡应携带可加入的卡名"
    add_button.click()
    expect(add_button).to_have_text("已加入牌表 ✓")
    expect(page.locator("#editor-dirty-note")).to_be_visible()

    # 打开牌表抽屉确认卡真的在编辑器里
    page.click("[data-deck-drawer-open]")
    drawer_list = page.locator("#deck-card-list")
    expect(drawer_list).to_contain_text(added_name)
