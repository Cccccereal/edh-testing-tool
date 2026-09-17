# 接口架构规划（2026-09-16 起草）

> 目的：项目从「单页 + 本地服务」长成了三个客户端共用的产品，接口需要从"顺手写"过渡到"有契约、有分层、可演进"。本文是路线图，不是最终规范；每期动工前再细化。

## 一、现状盘点

**客户端矩阵**（三个客户端跑的是同一个 Go 服务器 + 同一份前端）：

| 客户端 | 宿主 | 服务形态 | 前端来源 |
| --- | --- | --- | --- |
| 桌面浏览器 | — | `cmd/server`（DEV=1 或编译产物） | `cmd/server/web`（embed 或 DirFS） |
| 桌面壳 | `src-tauri`（Rust） | 拉起 server exe，WebView 加载 `127.0.0.1:<port>` | 同上（经 server 伺服） |
| 安卓 APK | gomobile bind | `cmd/mobile` 进程内起 server，WebView 加载 loopback | `cmd/mobile/web`（手工镜像） |

**API 面貌**：

- 11 个端点，全部挂在 `/api/v1` 下，同步 JSON；另有 `GET /healthz`。
- `internal/api/handler.go` 单文件 611 行，所有 handler + 校验 + 错误封装都在里面。
- 错误响应已有统一信封 `{error: {code, message}}`（好底子）。
- `/api/v1/analyze` 一次请求扛全部结果（约 10–17s，内部 errgroup 并发拉 5 个第三方源）。
- 无认证、无 CORS、仅 loopback —— 当前三客户端形态下这是合理默认，不是缺陷。
- `Analysis.DeckRevision`（牌表内容哈希）已经存在，是天然的幂等键 / 缓存键。

**已经欠下的债**：

1. handler.go 会继续膨胀（组合、缺件、交换、组牌……功能还在加）。
2. 前端两份拷贝（server/mobile）靠手工 `cp` 同步——8/28 差点漏掉，9/16 的按钮 bug 修复又得同步一次。这是流程性风险。
3. 请求/响应没有契约文档，前端 app.js 里 fetch 的字段全靠"对过一遍"。改后端字段只能靠肉眼 + 手点。
4. 10–17s 的同步长请求：超时敏感、无法断点续传、无法缓存复用、加载期只有一个静态骨架。

## 二、演进路线（四期，每期可独立交付）

### 第 1 期：分层整理（纯等价重构，无行为变化）

- `handler.go` 按域拆分：`analyze.go` / `build.go`（组牌三件套）/ `cards.go`（查询与自动补全）/ `swap.go`，公共逻辑（decode、writeError、大小限制）沉到 `api/common.go`。
- 引入轻量中间件链：request log、panic recover、per-route 超时。不引框架，`net/http` + 几个函数足够。
- 把工作区里已成型的 pytest 黑盒套件（`tests/` + `pytest.ini`，目前未入库）收编进仓库，作为接口行为的回归网。它就是第 2 期的地基。

### 第 2 期：契约化（OpenAPI 作为单一事实源）✅ 已落地（2026-09-17）

- 手写 `docs/api/openapi.yaml`（11 个端点规模不大，手写比代码生成更可控），请求/响应/错误码全部落 spec。
- CI 双向校验：pytest 用 `jsonschema` 断言真实响应符合 spec（防"实现漂移"）；spec 里的路径必须能在路由表里找到（防"文档漂移"）。
- 前端从 spec 生成类型（`openapi-typescript` → JSDoc `@type` 注解），纯 JS 也能获得编辑器提示，不引入构建链。
- 改字段的流程从此固定：先改 spec → 双端实现 → 测试守住。

> 落地细节：三道机器校验 = Go 路由同步测试（`internal/api/contract_test.go`）、
> pytest 契约测试（`tests/fixtures/` 联网录制快照离线复验 + 错误信封活校验）、
> CI 类型再生成防漂移；请求体 `additionalProperties: false` 对齐 Go 端
> `DisallowUnknownFields`。实测发现 sanitize 反射编码忽略 `omitempty`（响应字段
> 恒出现：空切片为 `[]`、指针为 `null`），spec 按实际行为描述并记入其头部「已知偏差」。

### 第 3 期：分析任务化（行为变化，动 `/analyze`）

- `POST /api/v1/analyses` → `202 {job_id}`；`GET /api/v1/analyses/{job_id}` → `{status, stage, result?}`。
- job 内存存储 + `DeckRevision` 去重：同一副牌重复分析直接命中，秒回。
- 阶段上报（loading → cards → combos → scoring）让骨架屏变成真进度；SSE 可选，轮询够用先不上。
- 旧同步 `POST /api/v1/analyze` 保留一个版本周期（标记 deprecated），三端客户端切完再删。

### 第 4 期（可选）：远程服务化

仅当产品方向决定"用户不用装客户端、开网页就用"时启动，否则是过度设计：

- 认证（匿名设备标识起步）、跨域 CORS、按 IP/设备的限流、结果 CDN 缓存。
- Tauri / APK 改连远程 or 保留本地双模式 —— 需要产品决策。

## 三、本期之外的顺手项

- **消灭手工镜像**：第 1 期顺手做——`go:generate` 或 `scripts/sync-web.ps1`（`cp server/web → mobile/web` + diff 校验），并加 CI 检查两目录必须一致，防再犯。
- pytest 套件入库时给 README 补运行说明（已写好）。

## 四、待决策点（不阻塞第 1、2 期）

1. 是否走第 4 期的远程服务化 —— 决定 job 化的存储设计（内存 vs 持久化）。
2. `/analyze` job 化的时机 —— 建议在第 2 期契约落地后再动，否则 spec 要写两遍。
3. 移动端离线场景（无第三方源可达时降级返回本地可算的部分）—— 现有 warnings 机制已部分覆盖，暂不展开。
