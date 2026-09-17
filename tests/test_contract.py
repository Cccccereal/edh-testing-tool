"""契约测试（离线）：真实响应快照必须符合 docs/api/openapi.yaml。

- tests/fixtures/*.json 是联网录制的成功响应（见 test_network_endpoints.py 的
  test_capture_fixtures，跑 `pytest -m network --update-fixtures` 重新录制）。
  本文件把它们逐字段对 spec 校验——CI 无外网也能验证「实现没有漂移」。
- 同时在线打各端点的错误分支（只到本地服务端校验为止，不触碰第三方站点），
  校验错误信封符合 spec 的 ErrorEnvelope 结构。
"""

import json
import pathlib

from helpers import response_schema, validate_against_contract

FIXTURE_DIR = pathlib.Path(__file__).resolve().parent / "fixtures"


def test_fixtures_match_contract(spec, spec_validator):
    files = sorted(FIXTURE_DIR.glob("*.json"))
    assert files, "fixtures 为空：请联网运行 `pytest -m network --update-fixtures` 录制"
    for file in files:
        record = json.loads(file.read_text(encoding="utf-8"))
        schema = response_schema(spec, record["path"], record["method"], record["status"])
        validate_against_contract(spec_validator, schema, record["body"], file.name)


def test_fixtures_cover_every_endpoint(spec):
    """每个契约端点都必须有 200 录制，防止新增端点绕过契约。"""
    recorded = (
        {json.loads(f.read_text(encoding="utf-8"))["path"] for f in FIXTURE_DIR.glob("*.json")}
        if FIXTURE_DIR.exists()
        else set()
    )
    for path in spec["paths"]:
        assert path in recorded, f"{path} 缺少 200 fixture（录制方法：pytest -m network --update-fixtures）"


def test_live_error_envelopes_match_contract(api, spec, spec_validator):
    envelope = spec["components"]["schemas"]["ErrorEnvelope"]
    # 全部是到达本地服务端校验就返回的分支，不触碰第三方站点
    cases = [
        ("POST", "/api/v1/analyze", {}),
        ("POST", "/api/v1/analyze", {"url": "not-a-url"}),
        ("POST", "/api/v1/analyze", {"url": "https://moxfield.com/decks/abcdefgh", "unexpected": 1}),
        ("POST", "/api/v1/compare-swap", {"decklist": "Deck\n1 Island"}),
        ("GET", "/api/v1/card", None),
        ("POST", "/api/v1/build-suggest", {}),
        ("POST", "/api/v1/build-lands", {}),
        ("POST", "/api/v1/build-staples", {}),
        ("GET", "/api/v1/commander-autocomplete", None),
        ("GET", "/api/v1/card-autocomplete", None),
        ("POST", "/api/v1/resolve-commanders", {}),
    ]
    for method, path, payload in cases:
        resp = api.request(method, path, json=payload)
        assert resp.status_code in (400, 404, 429, 502), (method, path, resp.status_code, resp.text[:200])
        body = resp.json()
        validate_against_contract(spec_validator, envelope, body, f"{method} {path} 错误信封")
