"""旅程①：输入 Moxfield 牌组 URL → 解析与双评分展示（全栈）。

真实浏览器提交表单，真实 Go 服务编排六源（全部指向本地 stub），
断言响应数据正确性（status / 双 provider / X-Cache）与前端渲染
（骨架屏、牌组名、双评分卡、健康徽章），并用 stub 计数验证
一次分析恰好触发一次上游编排。
"""

import pytest
from playwright.sync_api import expect

from journey_lib import CANONICAL_URL, TEXT_DECKLIST, open_home, submit_analyze, wait_for_results
from upstream_stub import CANONICAL_DECK_NAME

pytestmark = pytest.mark.ui


def test_url_analyze_journey(page, stubbed_server):
    stub = stubbed_server["stub"]["control"]
    stub.reset()

    open_home(page)
    response, payload = submit_analyze(page, url=CANONICAL_URL)

    # ---- 数据正确性（响应层） ----
    assert response.status == 200
    assert response.headers["x-cache"] == "miss", "全新牌组应真实回源编排一次"
    assert payload["status"] == "success"
    assert payload["warnings"] == []
    assert payload["deck"]["name"] == CANONICAL_DECK_NAME
    assert payload["results"]["commandersalt"]["status"] == "success"
    assert payload["results"]["edhpowerlevel"]["status"] == "success"
    assert payload["results"]["commandersalt"]["metrics"]["power_level"] == pytest.approx(5.4)

    # ---- 渲染正确性（DOM 层） ----
    wait_for_results(page)
    expect(page.locator("#edh-status")).to_have_text("完成")
    expect(page.locator("#salt-status")).to_have_text("完成")
    expect(page.locator("#deck-health-badge")).to_be_visible()
    expect(page.locator("#warning")).to_be_hidden()

    # ---- 上游编排恰好一次（stub 计数层） ----
    stats = stub.stats()
    assert stats["moxfield"] == 1, "一次分析应只加载一次牌组"
    assert stats["salt"] == 1
    assert stats["getcards"] == 1
    assert stats["edhrec"] == 1


def test_repeat_analyze_served_from_cache(page, stubbed_server):
    """同一牌组重新分析应命中 TTL/LRU 缓存：X-Cache=hit，且不再回源。

    用独立的 deck id 保证本用例独占冷缓存键——别的旅程已经分析过
    canonical 牌组，共用会拿到 hit 而失去「先 miss 后 hit」的对照。
    """
    stub = stubbed_server["stub"]["control"]
    stub.reset()
    url = "https://moxfield.com/decks/stubdeck02"

    open_home(page)
    response, first = submit_analyze(page, url=url)
    assert response.headers["x-cache"] == "miss"
    wait_for_results(page)
    assert first["deck_revision"]

    # 「重新分析」按钮只收起结果回到表单，真正重跑要再点一次提交
    page.evaluate("document.querySelector('#retry-button').click()")
    expect(page.locator("#results")).to_be_hidden()
    with page.expect_response(lambda r: "/api/v1/analyze" in r.url) as response_info:
        page.evaluate("document.querySelector('#submit-button').click()")
    repeat = response_info.value
    assert repeat.headers["x-cache"] == "hit", "同 key 二次分析应由缓存应答"
    assert repeat.json()["deck_revision"] == first["deck_revision"]

    stats = stub.stats()
    assert stats["moxfield"] == 1, "缓存命中不应再次回源 Moxfield"
    assert stats["salt"] == 1


def test_text_decklist_journey(page, stubbed_server):
    """文本录入模式：跳过 Moxfield，CommanderSalt 显式跳过而非失败。"""
    stub = stubbed_server["stub"]["control"]
    stub.reset()

    open_home(page)
    # 牌表文本面板收在 <details> 里，先展开再录入
    page.click(".decklist-input-panel summary")
    response, payload = submit_analyze(page, decklist=TEXT_DECKLIST)
    assert response.status == 200
    assert payload["status"] == "success"
    assert payload["results"]["commandersalt"]["status"] == "unavailable"
    assert payload["results"]["edhpowerlevel"]["status"] == "success"

    expect(page.locator("#edh-status")).to_have_text("完成")
    expect(page.locator("#salt-status")).to_have_text("跳过")

    stats = stub.stats()
    assert stats["moxfield"] == 0, "文本模式不应请求 Moxfield"
