"""旅程⑥：浏览器屏蔽站点数据时，核心旅程仍然可用。

Chrome 的「屏蔽所有 Cookie」会让 localStorage 访问抛 SecurityError。此前
脚本在加载时的主题读取上一抛即中止，之后所有事件绑定都没执行——点
「开始分析」毫无反应，页面彻底瘫掉。本机记忆（主题/视图选择/版本历史）
可以丢，页面不能瘫：所有存储访问都必须自带保护。
"""

import pytest
from playwright.sync_api import expect

from journey_lib import CANONICAL_URL, open_home, submit_analyze, wait_for_results

pytestmark = pytest.mark.ui


def test_analyze_survives_blocked_storage(page, stubbed_server):
    # 必须在首次导航前注入：init script 对之后的每次文档生效
    page.add_init_script(
        """
        const boom = () => { throw new DOMException('The operation is insecure.', 'SecurityError'); };
        Storage.prototype.getItem = boom;
        Storage.prototype.setItem = boom;
        Storage.prototype.removeItem = boom;
        Storage.prototype.clear = boom;
        """
    )
    open_home(page)

    # 落地页能交互：主题切换不炸（记忆写不进去就只在本次会话生效）
    page.click("#theme-toggle")
    expect(page.locator("#theme-toggle")).to_have_text("亮色")

    _, payload = submit_analyze(page, url=CANONICAL_URL)
    wait_for_results(page)
    assert payload["deck"]["card_count"] > 0

    # 分析后的关键交互同样不依赖存储：牌表抽屉能打开
    page.click("[data-deck-drawer-open]")
    expect(page.locator(".deck-drawer-panel")).to_be_visible()
    expect(page.locator("#deck-card-list .deck-row").first).to_be_visible()
