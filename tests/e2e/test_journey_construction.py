"""旅程②：构筑缺口报告（全栈）——六类指标的展示与 API 数据一致性。

断言前端渲染的指标卡与 analyze 响应里的 construction_report 完全一致
（标签、顺序、actual/target 数值），即「一处定义两处用」的数据正确层。
"""

import pytest
from playwright.sync_api import expect

from journey_lib import CANONICAL_URL, open_home, submit_analyze, wait_for_results

pytestmark = pytest.mark.ui


def test_construction_report_journey(page, stubbed_server):
    open_home(page)
    _, payload = submit_analyze(page, url=CANONICAL_URL)
    wait_for_results(page)

    report = payload["construction_report"]
    assert report, "stub 牌组应产出构筑缺口报告"

    section = page.locator("#construction-section")
    expect(section).to_be_visible()

    cards = page.locator("#construction-metrics .construction-metric")
    expect(cards).to_have_count(len(report["metrics"]))

    # 每张指标卡的标签与数值必须与 API 响应逐一对齐（顺序即渲染顺序）
    for index, metric in enumerate(report["metrics"]):
        card = cards.nth(index)
        expect(card.locator("summary span").first).to_have_text(metric["label"])
        expect(card.locator("summary strong")).to_have_text(
            f"{metric['actual']} / {metric['target']}"
        )
        bar = card.locator(".construction-bar i")
        expect(bar).to_have_attribute(
            "data-pct",
            str(min(100, round(metric["actual"] / max(1, metric["target"]) * 100))),
        )

    # stub 牌组的确定性事实：正向法力 = 36 张 Plains（Sol Ring 费用为 1，不计入 0 费快速法力）
    lands = next(m for m in report["metrics"] if m["id"] == "lands")
    assert lands["actual"] == 36
