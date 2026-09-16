"""POST /api/v1/analyze 的入参校验。

这些用例全部命中服务端的本地校验分支，不触发第三方站点请求，离线可测。
"""

import pytest

from helpers import error_code


@pytest.mark.parametrize(
    "payload",
    [
        {},
        {"url": "", "decklist": ""},
        {"url": "   "},
        {"decklist": "   "},
        {"url": "  ", "decklist": "  "},
    ],
)
def test_missing_deck_source(api, payload):
    resp = api.post("/api/v1/analyze", json=payload)

    assert resp.status_code == 400
    assert error_code(resp) == "MISSING_DECK_SOURCE"


def test_null_body_is_treated_as_missing_source(api):
    # JSON null 解码后是零值结构体：不算 INVALID_JSON，应落到缺少牌组来源
    resp = api.post(
        "/api/v1/analyze", data="null", headers={"Content-Type": "application/json"}
    )

    assert resp.status_code == 400
    assert error_code(resp) == "MISSING_DECK_SOURCE"


# Moxfield URL 校验：scheme、域名、路径形状、牌组 ID 字符集、端口与用户信息
BAD_MOXFIELD_URLS = [
    "moxfield.com/decks/AbCdEf",                    # 缺少 scheme
    "http://moxfield.com/decks/AbCdEf",             # 必须 https
    "https://github.com/decks/AbCdEf",              # 域名不对
    "https://moxfield.com/users/someone",           # 路径不是 /decks/{id}
    "https://moxfield.com/decks/",                  # 缺少牌组 ID
    "https://moxfield.com/decks/ab",                # ID 少于 6 位
    "https://moxfield.com/decks/abc$%^",            # ID 含非法字符
    "https://moxfield.com:8443/decks/AbCdEf",       # 不允许带端口
    "https://user:pass@moxfield.com/decks/AbCdEf",  # 不允许用户信息
    "https://moxfield.com/decks/AbCdEf/extra",      # 多余路径层级
]


@pytest.mark.parametrize("url", BAD_MOXFIELD_URLS)
def test_invalid_moxfield_url(api, url):
    resp = api.post("/api/v1/analyze", json={"url": url})

    assert resp.status_code == 400
    assert error_code(resp) == "INVALID_MOXFIELD_URL"


@pytest.mark.parametrize(
    "url",
    [
        "https://moxfield.com/decks/AbCdEf123",
        "https://www.moxfield.com/decks/AbCdEf123",
        "https://moxfield.com/decks/My-Deck_01",  # ID 允许大小写、数字、-、_
    ],
)
def test_moxfield_url_shape_is_accepted(api, url):
    """URL 形状合法时应通过校验（后续是否取到牌组取决于上游，这里只断言不再报 URL 错误）。"""

    resp = api.post("/api/v1/analyze", json={"url": url})

    assert resp.status_code != 400, resp.text


@pytest.mark.parametrize(
    "decklist",
    [
        "Commander\n1 Atraxa, Praetors' Voice\n\nDeck\n1x Sol Ring\nnot a card line",
        "Commander\n1 Atraxa, Praetors' Voice\n\nDeck\n0 Sol Ring",  # 数量为 0
        "Commander\n1 Atraxa, Praetors' Voice",  # 缺少主牌
        "1 Sol Ring",  # 只有一张牌：被当作主将后主牌为空
    ],
)
def test_invalid_decklist(api, decklist):
    resp = api.post("/api/v1/analyze", json={"decklist": decklist})

    assert resp.status_code == 400
    assert error_code(resp) == "INVALID_DECKLIST"


BAD_JSON_BODIES = [
    "not json at all",
    "[1, 2, 3]",
    '{"url": "https://moxfield.com/decks/AbCdEf", "unexpected_field": 1}',  # 未知字段
    '{"url": "a"} {"url": "b"}',  # 多个 JSON 对象
    "",  # 空请求体
]


@pytest.mark.parametrize("body", BAD_JSON_BODIES)
def test_invalid_json(api, body):
    resp = api.post(
        "/api/v1/analyze", data=body, headers={"Content-Type": "application/json"}
    )

    assert resp.status_code == 400
    assert error_code(resp) == "INVALID_JSON"


def test_oversized_body_is_rejected(api):
    # 请求体上限 256KB，超限应得到 400 而不是 413 或挂起
    huge_decklist = "A" * (300 * 1024)
    resp = api.post(
        "/api/v1/analyze",
        data='{"decklist": "' + huge_decklist + '"}',
        headers={"Content-Type": "application/json"},
    )

    assert resp.status_code == 400
    assert error_code(resp) == "INVALID_JSON"
