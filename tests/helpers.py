"""测试用的轻量 API 客户端与断言辅助。"""

import requests


class Api:
    """对 base_url 的薄封装，所有请求自带超时，避免服务端卡住时用例挂起。"""

    def __init__(self, base_url: str):
        self.base_url = base_url.rstrip("/")
        self.http = requests.Session()

    def get(self, path: str, **kwargs) -> requests.Response:
        kwargs.setdefault("timeout", 15)
        return self.http.get(self.base_url + path, **kwargs)

    def post(self, path: str, json=None, data=None, **kwargs) -> requests.Response:
        kwargs.setdefault("timeout", 15)
        return self.http.post(self.base_url + path, json=json, data=data, **kwargs)

    def request(self, method: str, path: str, **kwargs) -> requests.Response:
        kwargs.setdefault("timeout", 15)
        return self.http.request(method, self.base_url + path, **kwargs)


def error_code(resp: requests.Response) -> str:
    """断言响应符合统一错误结构 {error: {code, message}}，并返回 code。"""
    content_type = resp.headers.get("Content-Type", "")
    assert content_type.startswith("application/json"), (
        f"错误响应应为 JSON，实际 Content-Type: {content_type!r}，body: {resp.text[:200]!r}"
    )
    body = resp.json()
    assert set(body) == {"error"}, f"错误响应应只含 error 字段，实际: {body}"
    assert set(body["error"]) == {"code", "message"}, f"error 字段结构不符: {body['error']}"
    assert body["error"]["message"], "error.message 不应为空"
    return body["error"]["code"]


def response_schema(spec: dict, path: str, method: str, status: int) -> dict:
    """从契约里取出某端点某状态码的响应 schema（小写 method）。"""
    return spec["paths"][path][method]["responses"][str(status)]["content"]["application/json"]["schema"]


def validate_against_contract(validator_factory, schema: dict, body, label: str) -> None:
    """按契约校验响应体；失败时列出前 10 条路径化差异，便于定位字段。"""
    errors = list(validator_factory(schema).iter_errors(body))
    assert not errors, f"{label} 不符合契约:\n" + "\n".join(
        f"  {'/'.join(str(p) for p in err.absolute_path) or '<root>'}: {err.message}"
        for err in errors[:10]
    )
