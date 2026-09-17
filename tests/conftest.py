"""为 API 黑盒测试准备被测服务。

- 默认：在随机空闲端口上启动本项目的 Go 服务端（优先用 go build 编译当前源码，
  保证测的是最新代码；工具链不可用时退回仓库里的 server.exe），测试结束自动关闭。
- 已有服务在跑：设置环境变量 EDH_BASE_URL（如 http://127.0.0.1:18781）即可直连，
  不会另起进程。
"""

import os
import pathlib
import shutil
import socket
import subprocess
import sys
import tempfile
import time

import pytest
import requests

REPO_ROOT = pathlib.Path(__file__).resolve().parents[1]
STARTUP_TIMEOUT = 60.0

SPEC_PATH = REPO_ROOT / "docs" / "api" / "openapi.yaml"


def pytest_addoption(parser):
    parser.addoption(
        "--update-fixtures",
        action="store_true",
        default=False,
        help="network 用例把真实成功响应录制到 tests/fixtures/，供离线契约测试对 spec 校验",
    )


@pytest.fixture(scope="session")
def spec():
    """docs/api/openapi.yaml 解析结果——接口契约的单一事实源。"""
    import yaml

    with SPEC_PATH.open(encoding="utf-8") as fh:
        return yaml.safe_load(fh)


@pytest.fixture(scope="session")
def spec_validator(spec):
    """返回 make(schema)：以整份 spec 为 $ref 解析基准构造 JSON Schema 校验器。"""
    import warnings

    with warnings.catch_warnings():
        # RefResolver 在 jsonschema 4.x 标记弃用但仍可用；referencing 库迁移另行安排
        warnings.simplefilter("ignore", DeprecationWarning)
        from jsonschema import Draft202012Validator, RefResolver

        def make(schema):
            return Draft202012Validator(schema, resolver=RefResolver.from_schema(spec))

        return make


def _free_port() -> int:
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def _wait_ready(base_url: str, *, timeout: float) -> None:
    deadline = time.monotonic() + timeout
    last_error: Exception | None = None
    while time.monotonic() < deadline:
        try:
            resp = requests.get(base_url + "/healthz", timeout=2)
            if resp.status_code == 200:
                return
        except requests.RequestException as exc:
            last_error = exc
        time.sleep(0.2)
    raise RuntimeError(f"服务 {base_url} 在 {timeout:.0f}s 内未就绪（最后错误：{last_error}）")


def _build_binary() -> pathlib.Path | None:
    """优先编译当前源码，返回可执行文件路径；失败则返回 None 并打印编译错误。"""
    go = shutil.which("go")
    if not go:
        return None
    out_dir = pathlib.Path(tempfile.gettempdir()) / "edh-powerlevel-pytest"
    out_dir.mkdir(parents=True, exist_ok=True)
    binary = out_dir / "edh-test-server.exe"
    result = subprocess.run(
        [go, "build", "-o", str(binary), "./cmd/server"],
        cwd=REPO_ROOT,
        capture_output=True,
        text=True,
        timeout=180,
    )
    if result.returncode != 0:
        print(result.stderr, file=sys.stderr)
        return None
    return binary


def _stop(proc: subprocess.Popen) -> None:
    if sys.platform == "win32":
        # server 可能带子进程，用 taskkill 连树一起杀
        subprocess.run(["taskkill", "/PID", str(proc.pid), "/T", "/F"], capture_output=True)
    else:
        proc.terminate()
        try:
            proc.wait(timeout=5)
        except subprocess.TimeoutExpired:
            proc.kill()


@pytest.fixture(scope="session")
def base_url():
    external = os.environ.get("EDH_BASE_URL", "").strip().rstrip("/")
    if external:
        _wait_ready(external, timeout=10)
        yield external
        return

    binary = _build_binary()
    if binary is None:
        fallback = REPO_ROOT / "server.exe"
        if fallback.exists():
            binary = fallback
        else:
            pytest.fail("既没有可用的 go 工具链，也没找到仓库里的 server.exe，无法启动被测服务")

    port = _free_port()
    address = f"http://127.0.0.1:{port}"
    env = os.environ.copy()
    env["APP_ADDRESS"] = f"127.0.0.1:{port}"
    env["POWERLEVEL_OPEN_BROWSER"] = "0"  # 测试时不要弹出浏览器
    proc = subprocess.Popen(
        [str(binary)],
        cwd=REPO_ROOT,
        env=env,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
    )
    try:
        _wait_ready(address, timeout=STARTUP_TIMEOUT)
        yield address
    finally:
        _stop(proc)


@pytest.fixture(scope="session")
def api(base_url):
    from helpers import Api

    return Api(base_url)
