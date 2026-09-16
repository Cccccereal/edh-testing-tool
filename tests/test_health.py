"""GET /healthz —— 最基础的服务可用性探测。"""


def test_healthz_returns_ok(api):
    resp = api.get("/healthz")

    assert resp.status_code == 200
    assert resp.headers["Content-Type"].startswith("application/json")
    assert resp.json() == {"status": "ok"}


def test_healthz_is_stable_across_calls(api):
    assert api.get("/healthz").json() == {"status": "ok"}
    assert api.get("/healthz").json() == {"status": "ok"}
