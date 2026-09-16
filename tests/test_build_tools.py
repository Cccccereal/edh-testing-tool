"""组牌辅助接口（build-suggest / build-lands / build-staples / 自动补全 / resolve-commanders）的入参校验。

类别与名称的校验发生在联网查卡之前，离线可测。
"""

import pytest

from helpers import error_code


@pytest.mark.parametrize("payload", [{}, {"commander": ""}, {"commander": "   "}])
def test_build_suggest_requires_commander(api, payload):
    resp = api.post("/api/v1/build-suggest", json=payload)

    assert resp.status_code == 400
    assert error_code(resp) == "COMMANDER_REQUIRED"


def test_build_suggest_body_size_limit(api):
    # build-suggest 的请求体上限 64KB
    resp = api.post(
        "/api/v1/build-suggest",
        data='{"commander": "' + "A" * 70_000 + '"}',
        headers={"Content-Type": "application/json"},
    )

    assert resp.status_code == 400
    assert error_code(resp) == "INVALID_JSON"


@pytest.mark.parametrize(
    "payload",
    [{}, {"category": ""}, {"category": "   "}, {"category": "no-such-land-category"}],
)
def test_build_lands_requires_known_category(api, payload):
    resp = api.post("/api/v1/build-lands", json=payload)

    assert resp.status_code == 400
    assert error_code(resp) == "LAND_CATEGORY_REQUIRED"


@pytest.mark.parametrize(
    "payload",
    [
        {},
        {"category": ""},
        {"category": "   "},
        {"category": "no-such-staple-category"},
    ],
)
def test_build_staples_requires_known_category(api, payload):
    resp = api.post("/api/v1/build-staples", json=payload)

    assert resp.status_code == 400
    assert error_code(resp) == "STAPLE_CATEGORY_REQUIRED"


@pytest.mark.parametrize(
    "path", ["/api/v1/commander-autocomplete", "/api/v1/card-autocomplete"]
)
@pytest.mark.parametrize("query", [{}, {"q": ""}, {"q": "   "}])
def test_autocomplete_requires_query(api, path, query):
    resp = api.get(path, params=query)

    assert resp.status_code == 400
    assert error_code(resp) == "QUERY_REQUIRED"


@pytest.mark.parametrize("query", [{}, {"name": ""}, {"name": "   "}])
def test_lookup_card_requires_name(api, query):
    resp = api.get("/api/v1/card", params=query)

    assert resp.status_code == 400
    assert error_code(resp) == "CARD_NAME_REQUIRED"


@pytest.mark.parametrize("payload", [{}, {"commanders": []}, {"commanders": None}])
def test_resolve_commanders_requires_list(api, payload):
    resp = api.post("/api/v1/resolve-commanders", json=payload)

    assert resp.status_code == 400
    assert error_code(resp) == "COMMANDER_REQUIRED"


def test_resolve_commanders_rejects_non_array(api):
    resp = api.post("/api/v1/resolve-commanders", json={"commanders": "Atraxa"})

    assert resp.status_code == 400
    assert error_code(resp) == "INVALID_JSON"


def test_resolve_commanders_rejects_unknown_fields(api):
    resp = api.post(
        "/api/v1/resolve-commanders",
        json={"commanders": ["Sol Ring"], "unexpected_field": 1},
    )

    assert resp.status_code == 400
    assert error_code(resp) == "INVALID_JSON"
