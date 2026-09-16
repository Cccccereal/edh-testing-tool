"""需要访问第三方站点（Scryfall / Moxfield / EDHREC 等）的联调用例。

默认被 pytest.ini 的 addopts（-m "not network"）跳过；联网环境运行：

    pytest -m network

这些用例依赖外部服务的可用性与稳定性，失败不代表服务端代码有缺陷。
"""

import pytest

from helpers import error_code

pytestmark = pytest.mark.network


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
