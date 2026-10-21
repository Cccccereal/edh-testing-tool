"""旅程③：构筑缺口推荐（全栈）——分组默认收起 → 展开 → 直接加入构筑。

推荐数据来自 stub EDHREC 页面经真实推荐管线（缺口加权过滤）后的输出；
断言前端分组数与 API 一致、展开交互生效、「直接加入构筑」把卡写进编辑器。
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

    recommendations = payload["recommendations"]
    assert len(recommendations) >= 2, "stub EDHREC 页应至少留下两个推荐分组"

    # 分析完成后右侧牌表抽屉会自动展开遮住主页面，先收起再操作推荐区
    page.click("#deck-drawer-close")
    expect(page.locator("#deck-drawer")).to_be_hidden()

    section = page.locator("#recommendation-section")
    expect(section).to_be_visible()
    expect(page.locator("#recommendation-keywords")).to_contain_text("EDHREC 主题")

    groups = page.locator(".recommendation-group")
    expect(groups).to_have_count(len(recommendations))

    # 分组默认收起
    collapsed = re.compile(r".*is-collapsed.*")
    first = groups.nth(0)
    expect(first).to_have_class(collapsed)

    # 展开 → 推荐卡可见
    first.locator("[data-recommendation-toggle='0']").click()
    expect(first).not_to_have_class(collapsed)
    expect(first.locator(".recommendation-card").first).to_be_visible()

    # 「直接加入构筑」→ 按钮翻转 + 编辑器进入脏状态
    add_button = first.locator(".recommend-add").first
    added_name = add_button.get_attribute("data-add-name")
    assert added_name, "推荐卡应携带可加入的卡名"
    add_button.click()
    expect(add_button).to_have_text("已加入牌表 ✓")
    expect(page.locator("#editor-dirty-note")).to_be_visible()

    # 打开牌表抽屉确认卡真的在编辑器里
    page.click("[data-deck-drawer-open]")
    drawer_list = page.locator("#deck-card-list")
    expect(drawer_list).to_contain_text(added_name)
