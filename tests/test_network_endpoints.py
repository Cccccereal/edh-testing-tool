"""需要访问第三方站点（Scryfall / Moxfield / EDHREC 等）的联调用例。

默认被 pytest.ini 的 addopts（-m "not network"）跳过；联网环境运行：

    pytest -m network            # 联调冒烟 + 实时成功响应对契约校验
    pytest -m network --update-fixtures   # 上述用例之外，把成功响应录制进 tests/fixtures/

这些用例依赖外部服务的可用性与稳定性，失败不代表服务端代码有缺陷。
"""

import json
import pathlib

import pytest

from helpers import error_code, response_schema, validate_against_contract

pytestmark = pytest.mark.network

# analyze / compare-swap 共用的最小 Sram 牌表（有色主将 + 无色神器 + 纯地），
# 能覆盖 manabase / 构筑 / 健康度全部分析支路。
SRAM_DECKLIST = (
    "Commander\n"
    "1 Sram, Senior Edificer\n"
    "\n"
    "Deck\n"
    "1 Basalt Monolith\n"
    "1 Heliod, Sun-Crowned\n"
    "1 Walking Ballista\n"
    "1 Sol Ring\n"
    "20 Plains\n"
)

# 每个契约端点一条成功请求：(fixture 文件名, method, path, 额外 kwargs, 超时秒)。
# analyze 及组牌工具要串行访问第三方站点，超时放宽到服务端 REQUEST_TIMEOUT 以上。
CAPTURES = [
    ("healthz.json", "GET", "/healthz", {}, 15),
    ("analyze.json", "POST", "/api/v1/analyze", {"json": {"decklist": SRAM_DECKLIST}}, 180),
    (
        "compare_swap.json",
        "POST",
        "/api/v1/compare-swap",
        {"json": {"decklist": SRAM_DECKLIST, "remove_name": "Sol Ring", "add_name": "Mind Stone"}},
        180,
    ),
    ("card.json", "GET", "/api/v1/card", {"params": {"name": "Sol Ring"}}, 60),
    (
        "build_suggest.json",
        "POST",
        "/api/v1/build-suggest",
        {"json": {"commander": "Atraxa, Praetors' Voice", "count": 3}},
        180,
    ),
    (
        "build_lands.json",
        "POST",
        "/api/v1/build-lands",
        {"json": {"category": "fetch", "color_identity": ["W", "U"]}},
        60,
    ),
    (
        "build_staples.json",
        "POST",
        "/api/v1/build-staples",
        {"json": {"category": "ramp", "color_identity": ["W", "U"]}},
        60,
    ),
    ("commander_autocomplete.json", "GET", "/api/v1/commander-autocomplete", {"params": {"q": "atra"}}, 60),
    ("card_autocomplete.json", "GET", "/api/v1/card-autocomplete", {"params": {"q": "sol ring"}}, 60),
    ("random_commander.json", "POST", "/api/v1/random-commander", {}, 120),
    (
        "resolve_commanders.json",
        "POST",
        "/api/v1/resolve-commanders",
        {"json": {"commanders": ["Atraxa, Praetors' Voice"]}},
        60,
    ),
]


def test_commander_autocomplete(api):
    resp = api.get("/api/v1/commander-autocomplete", params={"q": "atraxa"})

    assert resp.status_code == 200, resp.text
    suggestions = resp.json()["suggestions"]
    assert suggestions, "主将自动补全不应为空"
    assert all(isinstance(name, str) for name in suggestions)
    assert any("Atraxa" in name for name in suggestions)


def test_card_autocomplete(api):
    resp = api.get("/api/v1/card-autocomplete", params={"q": "sol ring"})

    assert resp.status_code == 200, resp.text
    assert "Sol Ring" in resp.json()["suggestions"]


def test_resolve_commanders_returns_color_identity(api):
    resp = api.post(
        "/api/v1/resolve-commanders", json={"commanders": ["Atraxa, Praetors' Voice"]}
    )

    assert resp.status_code == 200, resp.text
    body = resp.json()
    assert body["commanders"], "应至少解析出一位主将"
    assert set(body["color_identity"]) == {"W", "U", "B", "G"}


def test_random_commander(api):
    resp = api.post("/api/v1/random-commander")

    assert resp.status_code == 200, resp.text
    assert resp.json(), "随机主将不应为空"


def test_build_lands_fetch_category(api):
    resp = api.post(
        "/api/v1/build-lands", json={"category": "fetch", "color_identity": ["W", "U"]}
    )

    assert resp.status_code == 200, resp.text
    assert resp.json()


def test_build_staples_game_changer_category(api):
    resp = api.post(
        "/api/v1/build-staples",
        json={"category": "game-changer", "color_identity": ["U"]},
    )

    assert resp.status_code == 200, resp.text
    assert resp.json()


def test_analyze_pasted_decklist_smoke(api):
    """粘贴牌表的完整分析冒烟用例：1 主将 + 1 Sol Ring + 9 Swamp = 11 张。

    完整分析要串行访问多个第三方站点，首跑可能远慢于普通接口，
    客户端超时放宽到服务端 REQUEST_TIMEOUT（90s）以上。
    """

    decklist = "Commander\n1 Ria Ivor, Bane of Bladehold\n\nDeck\n1x Sol Ring\n9 Swamp\n"
    resp = api.post("/api/v1/analyze", json={"decklist": decklist}, timeout=120)

    assert resp.status_code == 200, resp.text
    body = resp.json()
    assert body["deck"]["card_count"] == 11
    assert body["canonical_decklist"], "应返回规范化牌表"
    assert isinstance(body["results"], dict)


def test_analyze_moxfield_deck_not_found(api):
    """形状合法但实际不存在的牌组 ID：上游应返回找不到/受限类错误，而不是 200 或 500。"""

    resp = api.post("/api/v1/analyze", json={"url": "https://moxfield.com/decks/zzZZzz99xx"})

    assert resp.status_code in (502, 429), resp.text
    assert error_code(resp) in {
        "NOT_FOUND",
        "DECK_SOURCE_FAILED",
        "DECK_SOURCE_INVALID",
        "UPSTREAM_CHALLENGE",
        "PRIVATE_OR_FORBIDDEN",
        "RATE_LIMITED",
        "ANALYSIS_FAILED",
    }


def test_live_success_responses_match_contract(api, spec, spec_validator):
    """联网实时校验：每个端点的真实 200 响应逐字段符合契约。

    与离线的 test_contract.py 互补——那边校验录制下来的快照，这边校验当前
    上游数据（新卡牌字段、上游返回变化都可能让响应漂移）。
    """
    for _, method, path, kwargs, timeout in CAPTURES:
        resp = api.request(method, path, timeout=timeout, **kwargs)
        assert resp.status_code == 200, f"{method} {path}: {resp.text[:300]}"
        schema = response_schema(spec, path, method.lower(), 200)
        validate_against_contract(spec_validator, schema, resp.json(), f"{method} {path}")


def test_capture_fixtures(request, api, spec, spec_validator):
    """--update-fixtures 时录制全部端点的成功响应到 tests/fixtures/。

    录制前先对契约校验一遍，录制后清掉不再在 CAPTURES 名单里的陈旧文件。
    产物提交进仓库，供离线契约测试（test_contract.py）在 CI 使用。
    """
    if not request.config.getoption("--update-fixtures"):
        pytest.skip("加 --update-fixtures 才录制 fixtures")

    out_dir = pathlib.Path(__file__).resolve().parent / "fixtures"
    out_dir.mkdir(exist_ok=True)
    captured_names = set()
    for filename, method, path, kwargs, timeout in CAPTURES:
        resp = api.request(method, path, timeout=timeout, **kwargs)
        assert resp.status_code == 200, f"{method} {path}: {resp.text[:300]}"
        body = resp.json()
        schema = response_schema(spec, path, method.lower(), 200)
        validate_against_contract(spec_validator, schema, body, f"{method} {path}（录制时校验）")
        record = {"path": path, "method": method.lower(), "status": 200, "body": body}
        (out_dir / filename).write_text(
            json.dumps(record, ensure_ascii=False, indent=2) + "\n", encoding="utf-8"
        )
        captured_names.add(filename)

    for stale in out_dir.glob("*.json"):
        if stale.name not in captured_names:
            stale.unlink()


# ---------------------------------------------------------------------------
# /img 图片缓存代理（非契约路由，属传输层基础设施，见 internal/api/images.go）
# ---------------------------------------------------------------------------

SCRYFALL_IMAGE_HOST = "https://cards.scryfall.io"


def test_image_proxy_rejects_invalid_paths(api):
    """非法路径（空路径、未知扩展名）必须 400 + 标准错误信封。"""
    for bad in ["/img/", "/img/large/file.txt", "/img/large/front/a1/no-extension"]:
        resp = api.get(bad, timeout=15)
        assert resp.status_code == 400, f"{bad}: {resp.status_code} {resp.text[:200]}"
        assert error_code(resp) == "INVALID_IMAGE_PATH"


def test_image_proxy_serves_real_card_image(api):
    """经 /img 取真实卡图：200、图片类型、immutable 缓存头，且二次命中仍成功。

    图片 URL 取自 /api/v1/card 的真实响应，与前端 proxiedImage 的重写方式
    （去掉 https://cards.scryfall.io 前缀、加 /img）保持一致。
    """
    card = api.get("/api/v1/card", params={"name": "Sol Ring"}, timeout=60).json()
    image = card.get("image_small") or card.get("image_normal")
    assert image and image.startswith(SCRYFALL_IMAGE_HOST), f"卡图 URL 异常: {image!r}"
    proxied = "/img" + image[len(SCRYFALL_IMAGE_HOST):]

    for _ in range(2):  # 第一次回源，第二次应命中磁盘缓存
        resp = api.get(proxied, timeout=30)
        assert resp.status_code == 200, f"{proxied}: {resp.status_code} {resp.text[:200]}"
        assert resp.headers.get("Content-Type", "").startswith("image/"), resp.headers.get("Content-Type")
        assert resp.headers.get("Cache-Control") == "public, max-age=31536000, immutable"
        assert resp.content, "图片内容为空"
