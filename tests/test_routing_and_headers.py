"""路由方法约束、404 兜底、静态首页与全局安全响应头。"""

import pytest


# mux 上存在 "GET /" 兜底静态路由，方法语义如下：
# - 非 GET 方法访问任何路径都匹配不到模式 -> 405，并带 Allow 头列出允许的方法；
# - GET 访问 POST-only 的 API 路径时会被 "GET /" 接住 -> 落到静态文件处理 -> 404。
METHOD_MISMATCH = [
    ("POST", "/healthz"),
    ("DELETE", "/healthz"),
    ("PUT", "/api/v1/compare-swap"),
    ("PATCH", "/api/v1/compare-swap"),
    ("POST", "/api/v1/card"),
    ("DELETE", "/api/v1/random-commander"),
    ("POST", "/api/v1/commander-autocomplete"),
]


@pytest.mark.parametrize("method,path", METHOD_MISMATCH)
def test_method_mismatch_returns_405(api, method, path):
    resp = api.request(method, path)

    assert resp.status_code == 405
    assert "Allow" in resp.headers, "405 响应应带 Allow 头说明允许的方法"


@pytest.mark.parametrize(
    "method,path",
    [
        ("GET", "/api/v1/analyze"),  # POST-only 接口，GET 落入静态处理 -> 404
        ("GET", "/api/v1/build-suggest"),
        ("GET", "/api/v1/build-lands"),
        ("GET", "/api/v1/does-not-exist"),  # 未知路径同样由静态处理兜底
        ("GET", "/api/v1/"),
    ],
)
def test_get_falls_through_to_static_returns_404(api, method, path):
    assert api.request(method, path).status_code == 404


def test_unknown_route_with_non_get_returns_405(api):
    # 非 GET 方法即使路径不存在，也会因 "GET /" 的存在得到 405（Allow: GET）
    resp = api.request("POST", "/api/v1/does-not-exist")

    assert resp.status_code == 405
    assert resp.headers.get("Allow") == "GET, HEAD"  # Go 会为 GET 模式自动允许 HEAD


def test_index_page_served(api):
    resp = api.get("/")

    assert resp.status_code == 200
    assert "<html" in resp.text.lower()


@pytest.mark.parametrize("path", ["/healthz", "/api/v1/analyze"])
def test_security_headers_present(api, path):
    # /healthz 走正常 200，/api/v1/analyze 空请求体走 400，两条路径都要带安全头
    resp = api.post(path) if path == "/api/v1/analyze" else api.get(path)

    assert resp.status_code in (200, 400)
    assert resp.headers.get("X-Content-Type-Options") == "nosniff"
    assert resp.headers.get("X-Frame-Options") == "DENY"
    assert resp.headers.get("Referrer-Policy") == "no-referrer"
    assert resp.headers.get("Content-Security-Policy", "").startswith("default-src 'self'")
