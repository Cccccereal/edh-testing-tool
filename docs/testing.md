# 测试体系全景

《牌组平台测试体系规划.md》的落地实现：从用例设计到持续集成的完整测试链路。
本文是落地后的运行手册；各层均可在本地一条命令复跑。

## 分层总览

| 层 | 位置 | 被测对象 | 上游依赖 | 运行 |
|---|---|---|---|---|
| Go 单元/并发/模糊 | `internal/**/*_test.go` | 纯逻辑（解析器、评分、构筑、manabase、缓存语义） | 无 | `go test ./...` |
| API 契约黑盒 | `tests/test_*.py` | HTTP 契约（录制 fixtures 对 openapi.yaml 校验） | 无（离线） | `pytest -q` |
| UI 冒烟 | `tests/test_ui_*.py` | 前端布局/交互（route 拦截填充 fixtures） | 无（离线） | `pytest -q`（需 playwright） |
| **全栈 E2E** | `tests/e2e/` | 真实浏览器 + 真实 Go 编排 + stub 上游 | 本地 stub | `pytest tests/e2e -q` |
| **接口压测** | `perf/` | 服务层吞吐/时延/缓存/去重语义 | 本地 stub | `python perf/harness.py` |
| CI | `.github/workflows/ci.yml` | 以上全部（push/PR） | — | 自动 |
| Nightly | `.github/workflows/nightly.yml` | E2E 冒烟 + 压测基线回归 | — | 自动（北京 03:00）+ 手动 |

## 全栈 E2E（Playwright）

`tests/e2e/`：conftest 启动 stub（`tests/upstream_stub.py`）与被测服务
（六源环境变量全部指向 stub、独立临时 CACHE_DIR），Chromium 跑真实用户旅程。
每个旅程一个 spec，断言三层：响应数据（status/双 provider/X-Cache）、DOM 渲染、
stub 上游计数（一次分析恰好一次编排）。

| 旅程 | spec | 覆盖 |
|---|---|---|
| ① 输入 URL → 解析与双评分 | `test_journey_analyze.py` | 骨架屏、双评分卡、健康徽章、缓存 miss→hit、文本录入模式 |
| ② 构筑缺口报告 | `test_journey_construction.py` | 六类指标 UI 与 API 逐项对齐（一处定义两处用） |
| ③ 缺口推荐 | `test_journey_recommendations.py` | 分组收起/展开、「直接加入构筑」写回编辑器 |
| ④ 编辑器 → 即时测算 → 导出 | `test_journey_editor.py` | +/− 改牌、重新测算（新缓存 key，X-Cache=miss）、delta 对比表、牌表下载 |
| ⑤ 组牌器 | `test_journey_builder.py` | 主将补全、三选一、出地/常见单卡、组满 100 张导出 |

本地运行：`pip install playwright && python -m playwright install chromium`，然后
`pytest tests/e2e -q`（未装 playwright 自动跳过，`ui` marker）。

## 上游 stub（E2E 与压测共用地基）

`tests/upstream_stub.py`（纯 stdlib）：按路径模拟六个第三方源的响应形状，
对未知卡名合成合法数据；控制面 `GET /__stats`（各源计数）、`POST /__reset`、
`POST /__configure`（按源注入延迟/状态码，压测故障演练）。压测时用
`start_stub_pool(6)` 一源一端口——Go 客户端对单 host 有 MaxConnsPerHost=10 的
防突发限制，六源共端口会先撞客户端连接池而不是测出服务编排能力。

## 接口压测（k6）

见 `perf/README.md`。三个场景 + 软降级画像，指标与通过判据如下：

- **冷启动全源编排**：唯一 deck id 强制冷路径，RPS / P95 / P99 / partial 率；
  错误率 <2%、partial 率 <5%，P95 回归交基线闸门（±25%）。
- **软降级画像**（`--fault`）：salt 源延迟超过收缩的 PROVIDER_TIMEOUT →
  partial 率 >90% 且响应仍 200。
- **缓存命中 vs 冷查询**：按 X-Cache 分组，热 P95 < 800ms；实测收益约 137×
  （757ms → 5.5ms，规划书 66.9s→6.9s 基线的 stub 化复测）。
- **并发去重正确性**：50 并发同 key 恰好 1 miss + 49 coalesce，
  stub 上游各源恰好 1 次调用（singleflight 语义）。

## 可观测性：X-Cache 响应头

analyze 响应带 `X-Cache: miss | coalesce | hit`（`internal/service/analyzer.go`
标记、`internal/api/analyze.go` 设置，契约见 openapi.yaml）：
miss = 本次调用执行了上游编排；coalesce = 合并进在途 singleflight 请求；
hit = TTL/LRU 缓存直接应答。E2E 与压测靠它精确度量缓存收益与并发去重，
单测 `TestAnalyzeReportsCacheState` 钉死三态语义。

## 相关环境变量（测试/压测注入用）

| 变量 | 作用 | 默认 |
|---|---|---|
| `GETCARDS_URL` | 覆盖 EDH 评分 getcards 端点（指向 stub） | 内置生产端点 |
| `SPELLBOOK_MIN_NAME_DELAY` | Spellbook 按名限速间隔（stub 场景降到 10ms） | 1.5s |
| `CACHE_DIR` | 磁盘缓存根目录（测试用独立临时目录保证冷启动） | 系统缓存目录 |
| 六个 `*_API_URL` | 各上游端点（全部指向 stub） | 真实第三方 |
