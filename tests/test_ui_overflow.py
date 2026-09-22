"""UI 布局回归闸门（离线）：移动视口 + 系统字体缩放下，文字不得出框。

背景：安卓 WebView 的 textZoom 默认跟随系统「字体大小」设置（部分用户 1.3 倍），
字号变大而布局预算不变，历史上出现过文字顶破卡片（规则/评估 Bracket 标签被裁）、
面板标题行互相叠压、规则下拉面板伸出屏幕右缘等问题（见 styles.css 内注释）。

本文件用 Playwright 在 320/360px 视口 × 1.0/1.3 倍字号（重写 styles.css 的
px 字号，等价于 textZoom 的行为）下渲染落地页与分析结果页（analyze 响应用
tests/fixtures/analyze.json 拦截填充，不依赖上游），断言：
- 页面无横向溢出（scrollWidth == innerWidth）；
- 视口边界外没有非滚动容器内的元素；
- 「我想查查规则」下拉面板展开后也在视口内。

未安装 playwright 的环境自动跳过；需要被测服务，复用 conftest 的 base_url。
"""

import json
import pathlib
import re
import tempfile

import pytest

pytestmark = pytest.mark.ui

pw = pytest.importorskip("playwright")
from playwright.sync_api import sync_playwright  # noqa: E402

FIXTURE = json.loads(
    (pathlib.Path(__file__).resolve().parent / "fixtures" / "analyze.json").read_text(encoding="utf-8")
)

DECKLIST = (
    "Commander\n1 Sram, Senior Edificer\n\nDeck\n"
    "1 Basalt Monolith\n1 Heliod, Sun-Crowned\n1 Walking Ballista\n1 Sol Ring\n20 Plains\n"
)

# 视口宽度 × 字号倍率。320 覆盖小屏/系统「显示大小」调大的机型；1.3 覆盖
# 系统「字体大小：超大」。新增布局时若在此处失败，优先怀疑固定宽度/nowrap。
COMBOS = [(360, 1.0), (360, 1.3), (320, 1.0), (320, 1.3)]

# 视口边界外没有非滚动容器内的元素（导航条等 overflow-x:auto 容器内部是设计上的横滚，排除）。
SCAN_JS = """() => {
    const doc = document.scrollingElement;
    const offenders = [];
    for (const el of document.querySelectorAll('body *')) {
        const r = el.getBoundingClientRect();
        if (r.width <= 0) continue;
        if (r.right <= window.innerWidth + 1 && r.left >= -1) continue;
        let p = el.parentElement, scrollable = false;
        while (p && p !== document.body) {
            const cs = getComputedStyle(p);
            if (/(auto|scroll)/.test(cs.overflowX)) { scrollable = true; break; }
            p = p.parentElement;
        }
        if (scrollable) continue;
        offenders.push(`<${el.tagName} class="${el.className}"> [${(el.textContent || '').trim().slice(0, 30)}] ` +
            `left=${Math.round(r.left)} right=${Math.round(r.right)}`);
    }
    return { scrollW: doc.scrollWidth, innerW: window.innerWidth, offenders: offenders.slice(0, 10) };
}"""


def scaled_stylesheet(page, scale: float) -> str:
    """把 styles.css 里 font-size / font 简写的 px 值乘以 scale（textZoom 的等价行为）。"""
    css = page.evaluate("fetch('/styles.css').then((r) => r.text())")

    def repl(match: re.Match) -> str:
        return f"{match.group(1)}{round(float(match.group(2)) * scale, 1)}px"

    return re.sub(r"(font(?:-size)?\s*:\s*[^;}]*?)(\d+(?:\.\d+)?)px", repl, css)


def inject_font_scale(page, css_text: str) -> None:
    page.evaluate(
        """(cssText) => {
            let el = document.querySelector('#ui-test-font-scale');
            if (!el) {
                el = document.createElement('style');
                el.id = 'ui-test-font-scale';
                document.head.appendChild(el);
            }
            el.textContent = cssText;
        }""",
        css_text,
    )


def scan(page, label: str) -> dict:
    metrics = page.evaluate(SCAN_JS)
    assert metrics["scrollW"] <= metrics["innerW"], (
        f"{label}: 页面出现横向溢出 scrollWidth={metrics['scrollW']} > innerWidth={metrics['innerW']}"
    )
    assert not metrics["offenders"], (
        f"{label}: {len(metrics['offenders'])}+ 个元素超出视口边界 ->\n  "
        + "\n  ".join(metrics["offenders"])
    )
    return metrics


def save_failure_shot(page, tag: str) -> str:
    path = pathlib.Path(tempfile.gettempdir()) / f"edh-overflow-{tag}.png"
    page.screenshot(path=str(path), full_page=True)
    return str(path)


def test_mobile_viewports_have_no_text_overflow(base_url):
    with sync_playwright() as p:
        browser = p.chromium.launch(headless=True)
        try:
            for vw, scale in COMBOS:
                context = browser.new_context(
                    viewport={"width": vw, "height": 800},
                    device_scale_factor=2,
                    locale="zh-CN",
                )
                page = context.new_page()
                page.route(
                    "**/api/v1/analyze",
                    lambda route: route.fulfill(
                        status=200,
                        content_type="application/json",
                        body=json.dumps(
                            {k: FIXTURE["body"][k] for k in ("results", "deck", "canonical_decklist")}
                        ),
                    ),
                )
                page.goto(base_url)
                page.wait_for_load_state("networkidle")
                inject_font_scale(page, scaled_stylesheet(page, scale))
                page.wait_for_timeout(200)

                tag = f"{vw}px-x{scale}"
                scan(page, f"落地页 {tag}")

                # 「我想查查规则」下拉展开后也必须留在视口内（回归：曾固定 280px 伸出右缘）
                page.click(".rules-links-panel summary")
                page.wait_for_timeout(150)
                scan(page, f"落地页+规则下拉 {tag}")
                page.keyboard.press("Escape")
                page.evaluate("document.activeElement?.blur()")

                # 离线渲染结果页并复检（回歸：Bracket 卡标签被裁、面板标题行叠压）
                page.evaluate(
                    """(text) => {
                        const el = document.querySelector('#decklist-input');
                        el.value = text;
                        el.dispatchEvent(new Event('input', { bubbles: true }));
                        document.querySelector('#analyze-form').requestSubmit();
                    }""",
                    DECKLIST,
                )
                page.wait_for_selector("#results:not([hidden])", timeout=60000)
                page.evaluate("window.scrollTo(0, 0)")
                page.wait_for_timeout(200)
                try:
                    scan(page, f"结果页 {tag}")
                except AssertionError:
                    shot = save_failure_shot(page, tag)
                    raise AssertionError(f"截图已保存：{shot}") from None
                context.close()
        finally:
            browser.close()
