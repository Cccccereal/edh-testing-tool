# API 黑盒测试（pytest）

针对 Go 服务端 HTTP API 的黑盒用例，与 `internal/**/*_test.go` 的白盒单元测试互补。

## 运行

在仓库根目录（本目录上一级）执行：

```bash
# 默认：只跑离线用例（不访问第三方站点）
pytest
```

conftest 会自动把当前源码编译成临时二进制，并在随机空闲端口上启动一个服务端实例，
测试结束后自动关闭。也可以直连一个已经在跑的服务端：

```bash
# Windows PowerShell
$env:EDH_BASE_URL = "http://127.0.0.1:18781"; pytest

# Git Bash
EDH_BASE_URL=http://127.0.0.1:18781 pytest
```

依赖第三方站点（Scryfall / Moxfield / EDHREC…）的联调用例带 `network` 标记，
默认跳过，联网环境可用 `pytest -m network` 运行。

## 文件结构

| 文件 | 覆盖内容 |
| --- | --- |
| `conftest.py` | 被测服务的启动/复用与清理 |
| `helpers.py` | API 客户端封装与统一错误结构断言 |
| `test_health.py` | `/healthz` 健康检查 |
| `test_routing_and_headers.py` | 方法不匹配 405、404 兜底、静态首页、安全响应头 |
| `test_analyze_validation.py` | `/api/v1/analyze` 的 URL / 牌表 / JSON / 请求体大小校验 |
| `test_compare_swap.py` | `/api/v1/compare-swap` 的本地校验分支 |
| `test_build_tools.py` | 组牌辅助接口的必填项与类别校验 |
| `test_network_endpoints.py` | 依赖第三方站点的联调用例（默认跳过） |
