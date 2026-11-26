"""旅程 spec 的公共步骤：加载落地页、提交分析、等待结果渲染。"""

from upstream_stub import (
    CANONICAL_DECK_ID,
    CANONICAL_DECK_NAME,
    COMMANDER,
)

CANONICAL_URL = f"https://moxfield.com/decks/{CANONICAL_DECK_ID}"

# 文本录入旅程用的最小牌表（走同一份 stub 语料）
TEXT_DECKLIST = (
    "Commander\n"
    f"1 {COMMANDER}\n"
    "\n"
    "Deck\n"
    "1 Sol Ring\n"
    "1 Mentor of the Meek\n"
    "1 Swords to Plowshares\n"
    "36 Plains\n"
)


def open_home(page) -> None:
    from playwright.sync_api import expect

    page.goto("/index.html")
    expect(page.locator("#analyze-form")).to_be_visible()


def submit_analyze(page, *, url: str | None = None, decklist: str | None = None):
    """提交一次分析并返回 (response, payload)。

    骨架屏用 MutationObserver 记录「至少立起过一次」：缓存命中时响应
    几十毫秒就回来，骨架的显→隐翻转会快过轮询式断言，直接断
    to_be_visible 会闪断。
    """
    if url is not None:
        page.fill("#deck-url", url)
    if decklist is not None:
        page.fill("#decklist-input", decklist)
    page.evaluate(
        """() => {
          window.__skeleton_shown = false;
          const el = document.querySelector('#skeleton');
          const record = () => { if (!el.hidden) window.__skeleton_shown = true; };
          record();
          new MutationObserver(record).observe(el, {attributes: true, attributeFilter: ['hidden']});
        }"""
    )

    with page.expect_response(lambda r: "/api/v1/analyze" in r.url) as response_info:
        page.click("#submit-button")
    response = response_info.value
    payload = response.json()
    assert page.evaluate("window.__skeleton_shown"), "提交后骨架屏应至少展示一次"
    return response, payload


def wait_for_results(page):
    from playwright.sync_api import expect

    expect(page.locator("#results")).to_be_visible()
    expect(page.locator("#deck-name")).to_have_text(CANONICAL_DECK_NAME)


def read_clipboard_text(page) -> str:
    """读回剪贴板文本，行尾统一成 LF。

    导出牌表走剪贴板（应用写的是 LF），但 Windows 剪贴板会把它存成 CRLF，
    读回来直接和 LF 文本比较会不相等。
    """
    return page.evaluate("navigator.clipboard.readText()").replace("\r\n", "\n")


__all__ = [
    "CANONICAL_DECK_NAME",
    "CANONICAL_URL",
    "TEXT_DECKLIST",
    "open_home",
    "read_clipboard_text",
    "submit_analyze",
    "wait_for_results",
]
