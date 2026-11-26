"""旅程⑦：放大层压在抽屉上时 Esc 只关最上层。

此前两个 keydown 监听器对同一个按键都响应：关放大层的 Esc 把抽屉一起
关掉；反过来，closeCardZoom 无条件解锁页面，抽屉还开着页面却能滚了。
"""

import pytest
from playwright.sync_api import expect

from journey_lib import CANONICAL_URL, open_home, submit_analyze, wait_for_results

pytestmark = pytest.mark.ui


def test_zoom_escape_layers_above_the_drawer(page, stubbed_server):
    open_home(page)
    submit_analyze(page, url=CANONICAL_URL)
    wait_for_results(page)

    page.click("[data-deck-drawer-open]")
    expect(page.locator(".deck-drawer-panel")).to_be_visible()

    # 抽屉开着时从一行牌打开放大层
    page.locator("#deck-card-list .deck-row-main").first.click()
    expect(page.locator("#card-zoom")).to_be_visible()

    # 第一个 Esc：只关放大层；抽屉保持，滚动锁也保持
    page.keyboard.press("Escape")
    expect(page.locator("#card-zoom")).to_be_hidden()
    expect(page.locator(".deck-drawer-panel")).to_be_visible()
    assert page.evaluate("document.body.style.overflow") == "hidden"

    # 第二个 Esc：这才关抽屉，页面解锁
    page.keyboard.press("Escape")
    expect(page.locator("#deck-drawer")).to_be_hidden()
    assert page.evaluate("document.body.style.overflow") == ""
