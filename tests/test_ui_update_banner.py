"""UI 更新横幅行为测试（离线，Playwright 拦截 /api/v1/version）。

覆盖 app.js 的更新提示逻辑：
- 服务端报告更新的发布版本时，页面底部出现横幅，链接指向发布页；
- 点 × 忽略后写入 localStorage，刷新页面不再出现（同版本）；
- current 为 "dev"、latest 为 null、latest 不比 current 新时，不出现横幅。

未安装 playwright 的环境自动跳过；复用 conftest 的 base_url。
"""

import pytest

pytestmark = pytest.mark.ui

pw = pytest.importorskip("playwright")
from playwright.sync_api import sync_playwright  # noqa: E402


def mock_version(payload):
    """返回 /api/v1/version 路由拦截器；payload 为 None 时模拟端点 500。"""
    def handle(route):
        if payload is None:
            route.fulfill(status=500, content_type="application/json", body="{}")
        else:
            import json
            route.fulfill(status=200, content_type="application/json", body=json.dumps(payload))
    return handle


def open_page(page, base_url, payload):
    page.route("**/api/v1/version", mock_version(payload))
    page.goto(base_url)
    page.wait_for_load_state("networkidle")
    page.wait_for_timeout(150)


def test_update_banner_flow(base_url):
    newer = {
        "current": "v20260901-0800",
        "latest": {
            "version": "v20260922-1030",
            "notes": "修复若干问题",
            "release_url": "https://example.com/releases/v20260922-1030",
        },
    }
    with sync_playwright() as p:
        browser = p.chromium.launch(headless=True)
        try:
            # 1) 有新版本：横幅出现，文案与链接正确，notes 进 title
            context = browser.new_context(viewport={"width": 1280, "height": 800})
            page = context.new_page()
            open_page(page, base_url, newer)
            banner = page.locator("#update-banner")
            assert banner.is_visible(), "新版本场景下横幅应出现"
            assert "v20260922-1030" in banner.inner_text() and "v20260901-0800" in banner.inner_text()
            link = banner.locator("a")
            assert link.get_attribute("href") == "https://example.com/releases/v20260922-1030"
            assert "修复若干问题" in (banner.locator("span").first.get_attribute("title") or "")

            # 2) 点 ×：横幅消失，刷新后仍不出现（localStorage 记忆）
            banner.locator("button.update-banner-dismiss").click()
            assert banner.count() == 0 or not banner.is_visible(), "忽略后横幅应消失"
            page.reload()
            page.wait_for_load_state("networkidle")
            page.wait_for_timeout(150)
            assert not page.locator("#update-banner").is_visible(), "忽略后刷新不应再出现"
            context.close()

            # 3) 无新版本 / dev 构建 / 端点失败：都不出现横幅
            for name, payload in [
                ("版本相同", {"current": "v20260922-1030", "latest": {**newer["latest"]}}),
                ("latest 为 null", {"current": "v20260901-0800", "latest": None}),
                ("dev 构建", {"current": "dev", "latest": newer["latest"]}),
                ("端点失败", None),
            ]:
                context = browser.new_context(viewport={"width": 1280, "height": 800})
                page = context.new_page()
                open_page(page, base_url, payload)
                assert not page.locator("#update-banner").is_visible(), f"{name} 不应出现横幅"
                context.close()

            # 4) 版本页脚：非 dev 显示构建 tag
            context = browser.new_context(viewport={"width": 1280, "height": 800})
            page = context.new_page()
            open_page(page, base_url, newer)
            assert page.locator("#app-version").inner_text() == "版本 v20260901-0800"
            context.close()
        finally:
            browser.close()
