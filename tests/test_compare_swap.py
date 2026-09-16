"""POST /api/v1/compare-swap 的入参校验。

用一张合法小牌表驱动服务端在联网查卡之前的本地校验分支（同牌、主将、移除牌不在场），
这些用例离线可测。
"""

import pytest

from helpers import error_code


DECK = "Commander\n1 Ria Ivor, Bane of Bladehold\n\nDeck\n1x Sol Ring\n9 Plains\n"


@pytest.mark.parametrize(
    "payload",
    [
        {"decklist": "", "remove_name": "Sol Ring", "add_name": "Cyclonic Rift"},
        {"decklist": "   ", "remove_name": "Sol Ring", "add_name": "Cyclonic Rift"},
        {"decklist": DECK, "remove_name": "", "add_name": "Cyclonic Rift"},
        {"decklist": DECK, "remove_name": "   ", "add_name": "Cyclonic Rift"},
        {"decklist": DECK, "remove_name": "Sol Ring", "add_name": ""},
        {"decklist": DECK, "remove_name": "Sol Ring", "add_name": "  "},
    ],
)
def test_swap_missing_fields(api, payload):
    resp = api.post("/api/v1/compare-swap", json=payload)

    assert resp.status_code == 400
    assert error_code(resp) == "INVALID_SWAP"


def test_swap_same_card_rejected(api):
    # 移除牌和加入牌是同一张（大小写不敏感）
    resp = api.post(
        "/api/v1/compare-swap",
        json={"decklist": DECK, "remove_name": "Sol Ring", "add_name": "SOL RING"},
    )

    assert resp.status_code == 400
    assert error_code(resp) == "INVALID_SWAP"


def test_swap_commander_not_supported(api):
    resp = api.post(
        "/api/v1/compare-swap",
        json={
            "decklist": DECK,
            "remove_name": "Ria Ivor, Bane of Bladehold",
            "add_name": "Sol Ring",
        },
    )

    assert resp.status_code == 400
    assert error_code(resp) == "COMMANDER_SWAP_NOT_SUPPORTED"


def test_swap_remove_card_not_in_mainboard(api):
    resp = api.post(
        "/api/v1/compare-swap",
        json={"decklist": DECK, "remove_name": "Black Lotus", "add_name": "Sol Ring"},
    )

    assert resp.status_code == 400
    assert error_code(resp) == "REMOVE_CARD_NOT_FOUND"


def test_swap_with_invalid_decklist(api):
    resp = api.post(
        "/api/v1/compare-swap",
        json={
            "decklist": "not a card line",
            "remove_name": "Sol Ring",
            "add_name": "Cyclonic Rift",
        },
    )

    assert resp.status_code == 400
    assert error_code(resp) == "INVALID_DECKLIST"


@pytest.mark.parametrize(
    "body",
    [
        "not json",
        '{"decklist": "x", "unexpected_field": 1}',  # 未知字段
        '{"a":1} {"b":2}',  # 多个 JSON 对象
    ],
)
def test_swap_invalid_json(api, body):
    resp = api.post(
        "/api/v1/compare-swap", data=body, headers={"Content-Type": "application/json"}
    )

    assert resp.status_code == 400
    assert error_code(resp) == "INVALID_JSON"
