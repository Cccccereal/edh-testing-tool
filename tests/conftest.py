"""为 API 黑盒测试准备被测服务。

- 默认：在随机空闲端口上启动本项目的 Go 服务端（优先用 go build 编译当前源码，
  保证测的是最新代码；工具链不可用时退回仓库里的 server.exe），测试结束自动关闭。
- 已有服务在跑：设置环境变量 EDH_BASE_URL（如 http://127.0.0.1:18781）即可直连，
  不会另起进程。
"""

import os
import pathlib
import subprocess

import pytest
import requests

from helpers import build_server_binary, free_port, stop_process, wait_ready

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


@pytest.fixture(scope="session")
def base_url():
    external = os.environ.get("EDH_BASE_URL", "").strip().rstrip("/")
    if external:
        wait_ready(external, timeout=10)
        yield external
        return

    binary = build_server_binary()
    if binary is None:
        fallback = REPO_ROOT / "server.exe"
        if fallback.exists():
            binary = fallback
        else:
            pytest.fail("既没有可用的 go 工具链，也没找到仓库里的 server.exe，无法启动被测服务")

    port = free_port()
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
        wait_ready(address, timeout=STARTUP_TIMEOUT)
        yield address
    finally:
        stop_process(proc)


@pytest.fixture(scope="session")
def api(base_url):
    from helpers import Api

    return Api(base_url)
