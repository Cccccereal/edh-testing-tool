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

## 接口契约（docs/api/openapi.yaml）

`docs/api/openapi.yaml` 是接口的单一事实源。契约由三道机器校验守住：

1. `go test ./internal/api/`：路由表与 spec paths 双向同步；
2. `pytest tests/test_contract.py`：`fixtures/` 里录制的真实成功响应逐字段对 spec
   校验 + 在线错误响应符合 `ErrorEnvelope`（全部离线可跑，CI 依赖）；
3. `npm run gen:api`：前端类型（`cmd/server/web/api-types.d.ts`）再生成无 diff
   （CI 里跑，防手改生成文件）。

fixtures 是联网录制的快照。接口或上游数据变了导致快照失真时，联网环境运行：

```bash
pytest -m network --update-fixtures
```

会把全部端点的 200 响应重新录制（录制时即时对 spec 校验），提交新快照即可。

## 文件结构

| 文件 | 覆盖内容 |
| --- | --- |
| `conftest.py` | 被测服务的启动/复用与清理；spec 加载与 JSON Schema 校验器；`--update-fixtures` 选项 |
| `helpers.py` | API 客户端封装、统一错误结构断言、契约 schema 取用与校验 |
| `test_health.py` | `/healthz` 健康检查 |
| `test_routing_and_headers.py` | 方法不匹配 405、404 兜底、静态首页、安全响应头 |
| `test_analyze_validation.py` | `/api/v1/analyze` 的 URL / 牌表 / JSON / 请求体大小校验 |
| `test_compare_swap.py` | `/api/v1/compare-swap` 的本地校验分支 |
| `test_build_tools.py` | 组牌辅助接口的必填项与类别校验 |
| `test_contract.py` | 接口契约：fixtures 对 spec 逐字段校验、端点覆盖检查、错误信封活校验 |
| `test_network_endpoints.py` | 依赖第三方站点的联调用例 + 实时成功响应对契约校验 + fixtures 录制 + /img 卡图代理（默认跳过） |
| `fixtures/` | 联网录制的各端点 200 响应快照（提交入库，供离线契约测试与 CI 使用） |

