# Builder Demo 任务要点记录

> 记录于 2026-08-26。本文档记录近期开发任务的要点、根因与决策，便于后续接手时快速定位。

## 今日任务概述

1. **重启服务并确保运行最新代码**（此前 `start.ps1` 启动的是预编译的旧二进制，前端/后端改动未生效）
2. **解决 Commander Spellbook API 限流问题**，并保证组合制胜负手（wincon）检测不丢

---

## 一、服务重启与「改动了但没生效」的根因

- `start.ps1` 启动的是**预编译二进制** `.run/powerlevel-server.exe`，不是 `go run`。
- 前端文件通过 `//go:embed web/*` 编译进二进制，因此**改前端也必须重新 `go build`**，否则只改源码不重启等于白改。
- 历史教训：端口上同时挂着两个旧 `go run` 进程（0.0.0.0:18781 与 127.0.0.1:18781），导致新旧版本混跑。已清理。
- **固定操作流程**：`go build -o .run/powerlevel-server.exe ./cmd/server` → `./start.ps1 -Stop` → `./start.ps1`。

## 二、Spellbook 限流问题的探测结论

**429 不是按「请求次数」算，而是按「短窗口内的突发速率」判定。**

| 行为 | 结果 |
|---|---|
| 单个 / 少量请求 | 总是 200 |
| 十来个请求快速连发（Go client 并发或极快串行） | **整批 429**，连之前一直成功的名字也中招 |
| 被打后等待约 45 秒 | 同一请求立刻恢复 200 |
| 恢复后 0.8s 间隔串行打 10 个 | 全 200（非固定速率限制，更像窗口累计突发检测） |
| **1.5s 间隔串行打 13 个**（Go / Python 双验证） | **全 200** |

结论：`sleep` 冷却不可靠，**缓存 + 分页 + 节流**才是稳定方案。

## 三、已实施的限流缓解（全部已上线）

1. **每卡名缓存（10 分钟 TTL）**：同一卡组重复分析不再重新请求；跨卡组公共卡名复用。
2. **分页抓取**：API 单页上限 12，3+ 卡组合挤满首页时会挡住后面的 2 卡组合；用 `offset` 翻页让胜负组合浮出。
3. **只保留 2 卡组合**：3+ 卡组合在 `fetchPage` 抓取时直接跳过，不浪费 12 个名额。
4. **跳过被限流的卡名**：某个名字 429 时跳过它继续查其余名字，不让整批归零。
5. **请求节流 `minNameDelay = 1.5s`**（本次新增）：仅对**未命中缓存**的卡名生效，保证两次真实请求间隔 ≥1.5s；命中缓存直接跳过等待。等待可被 context 取消。

**权衡**：首次冷缓存分析约 13 个卡名 × 1.5s ≈ 20 秒用于节流，首次稍慢；后续命中缓存大幅加速（实测：第一次 66.9s → 第二次 6.9s）。

## 四、wincon_combo_count 一直为 0 的根因与修复

- 根因：`Report.WinconComboCount` 字段在 `internal/service/construction/report.go` 中**声明了但从未被赋值**；`ApplyWinconCombos` 只改了指标数值。
- 修复：在 `ApplyWinconCombos` 顶部加 `r.WinconComboCount = count`。
- 组合胜负手判定：组合 `Result` 中出现 "win the game" / "opponents lose the game"（大小写不敏感，按逗号分隔逐特征匹配）即计入。
- 分析流程顺序已调整：卡牌目录 → 构建报告 → **Spellbook 组合查询** → 组合修正 wincon → **健康分（health）**，确保健康分看到组合修正后的胜负手。

## 五、验证结果（Rogsi 测试卡组）

```
Commander
1 Rograkh, Son of Rohgahh
1 Silas Renn, Seeker Adept

Deck
1 Tainted Pact
1 Thassa's Oracle
1 Demonic Consultation
1 Underworld Breach
1 Lion's Eye Diamond
1 Brain Freeze
1 Grinding Station
1 Wheel of Fortune
1 Jeska's Will
1 Lotus Petal
92 Island
```

- 冷缓存全卡组分析：combos 12 个，`wincon_combo_count: 8`，胜负手指标 `9/4 met`。
- Tainted Pact + Thassa's Oracle、Demonic Consultation + Thassa's Oracle 等胜负组合正常返回。
- 18781 托管服务当前运行新二进制（含全部修复），PID 见 `.run/powerlevel.pid`。

## 六、技术决策记录

- **前端不引入 React/Vue**：前端是 `//go:embed` 打进的静态文件，无构建步骤；引入框架必须加 node 构建管线并接入 Go embed 流程，成本高收益低。当前 2600 行 vanilla JS 完全够用。等前端长到四五千行、状态共享变复杂时再考虑迁移。
- **推送**：今日改动已提交并 push 到 `origin/main`。

## 七、相关文件

| 文件 | 说明 |
|---|---|
| `internal/providers/spellbook/client.go` | 缓存 / 分页 / 2卡过滤 / 跳过限流 / 节流（本次核心） |
| `internal/providers/spellbook/client_test.go` | 对应单元测试（含节流测试） |
| `internal/service/analyzer.go` | 分析流程顺序：组合查询在健康分之前 |
| `internal/service/construction/report.go` | `WinconComboCount` 字段赋值修复 |
| `cmd/server/main.go` / `cmd/mobile/mobile.go` | `//go:embed web/*` 嵌入前端 |
| `start.ps1` | 托管服务启停脚本（预编译二进制） |

---

# 2026-08-27 续记

## 今日任务概述

1. **分析流水线 errgroup 并发化** + Spellbook 节流字段的数据竞争修复（`9b5a48b`）
2. **健康徽章只显示 0-100 分数**，去掉与指挥官分级术语混淆的字母（`56f0658`）
3. **平面设计风格整理成本地 skill**，前端按「纸片层」思路加厚（skill 本地私有；UI 改动 `acae95b`）
4. **滚动淡入 bug 修复** + 关联卡牌/组合区块默认收起（`bd082f3`）
5. **悬停箭头彩蛋两版实现后放弃**（负结果记录，含日后重启方案）
6. **按钮箭头改朝下 + 点击后慢滚加载区**（`e2d02c7`、`4f62f8d`）
7. **启用 DEV=1 开发模式**，前端改动免重建（修正昨日「改前端必须 go build」的结论）

以上代码改动均已 push 至 `origin/main`。

## 一、分析流水线四路并发（9b5a48b）

- `analyze()` 中四个**互不依赖**的数据源改为 `errgroup.Group` 并行：CommanderSalt 评分、Scryfall 目录、EDHREC（含依赖它的候选查询）、Spellbook 组合。
- 卡组加载保持串行；`group.Wait()` 之后**仍按原串行顺序**处理各路结果 → 警告顺序、partial 语义与旧行为完全一致。
- 顺手修了 `spellbook/client.go` 的 `lastFetch` **无锁读写竞争**：改为锁内预留等待槽位（`lastFetch = now + wait`）、锁外 sleep，等待可被 context 取消。
- 新增 `TestAnalyzeFetchesIndependentProvidersConcurrently`（barrier 型 fake：所有数据源就绪才放行），对串行实现必红（"only 1 of 3 providers started"），曾 stash 实现验证过测试有效性。
- E2E 行为等价验证：串行 17.6s → 并行 13.5s，响应 JSON 完全一致（partial、相同 warnings / combos / recommendations）。
- 环境坑：**本机 `-race` 不可用**（报 0xc0000139，mingw 8.1 过旧、不配 Go 1.26 race runtime，与代码无关）；以 `-count=3` 重复测试 + E2E 对比替代。

## 二、健康徽章去字母（56f0658）

- 徽章只渲染 `0-100` 数字；`grade` 字段保留，仅用于无数据时隐藏与配色 class（`health-grade-x`），不再输出字母，避免与指挥官 bracket（1-5 级）分级用语混淆。

## 三、设计风格 skill 与「纸片层」加厚（本地 + acae95b）

- 学了 zcool 与 mew.design 两篇风格综述，整理成 `resourse/graphic-design-styles/`（SKILL.md 按意图选型 + styles.md 29 风格档案）。`resourse/` 在 gitignore 内，**保持本地私有，不入库**。
- 第一轮扁平化被否（「显得单薄」），用户提出「按钮底下垫一层」→ 定下**纸片层深度原则**：
  - **L0** 平面数据区：不加阴影；
  - **L1** 硬偏移纸片：实色偏移阴影（3–12px、无模糊无渐变）；hover 抬起（位移 -1,-1、阴影变大），active 落座（位移到阴影尺寸、阴影归零）；
  - **L2** 真浮层（tooltip、吸顶导航）才允许 blur。
- 落地：ghost-button 3px、submit-button 4px 实色、result-card 7→10px、搜索面板去玻璃改实色 + 12px 阴影、英雄区酸绿圆环（三角按用户要求删除）。

## 四、滚动淡入修复与默认收起（bd082f3）

- 根因：`intersectionRatio >= 0.25` 的触发阈值，对**高度超过约 4 倍视口**的区块（关联卡牌、完整牌表）在数学上不可达 → 永远不淡入。
- 修复：任意相交即淡入、完全离开视口才淡出，`dataset.scrollAnimated` 防重复初始化。
- 关联卡牌与组合区块在新分析渲染后**默认收起**（`setSectionCollapsed('combo-section', true)`），不再挤占首屏。

## 五、悬停箭头彩蛋：两版实现后放弃（未提交，已还原）

- v1：CSS motion path（offset-path）让字形飞一圈——与手绘草稿差距大，被否。
- v2：SVG `pathLength=1` + stroke-dashoffset 描边自绘，`preserveAspectRatio=none` + `non-scaling-stroke` 适配任意面板宽度，HTML 三角笔尖收尾——仍与期望有差距。
- **放弃根因**：起点要求精确踩在步骤方块上，但方块与按钮分属不同布局行；锚定输入行的 SVG 在 `preserveAspectRatio=none` 下横向拉伸，起点位置随面板宽度漂移，纯比例估算对不准。
- 若日后再做：SVG 改挂**整个搜索面板**统一坐标系，或 hover 时用 JS 读方块与按钮实际位置现算路径。
- 处理：4 个前端文件 `git checkout --` 还原到 `bd082f3`；因为从未提交，工作区直接回到干净态。

## 六、按钮箭头朝下 + 慢滚加载区（e2d02c7 + 4f62f8d）

- 「开始分析」「我其实没有牌，想组牌」的 `↗` 改 `↓`，语义指向页面下方的内容；外链的 `↗` 不动。
- `scrollToSlowly()`：rAF 补间 1.1s easeInOutQuad 滚到 `#loading`。不用原生 `smooth`：时长不可控、一晃而过，骨架屏来不及进入视野。结果渲染后的原有平滑滚动保持不变。

## 七、DEV=1 开发模式（修正昨日流程结论）

- `cmd/server/main.go:89`：`DEV=1` 时用 `os.DirFS("cmd/server/web")` 从磁盘伺服前端 → **改前端只需刷新浏览器，无需 go build / 重启**。昨日「改前端也必须重新 build」只在 embed（生产）模式下成立。
- 两个注意点：dev 模式只读桌面目录，`cmd/mobile/web` 镜像副本要靠 `diff` 手动保持同步；改 Go 代码仍需重启进程。
- 当前预览：`DEV=1 APP_ADDRESS=:18792 go run ./cmd/server`（后台运行）。

## 八、今日相关文件

| 文件 | 说明 |
|---|---|
| `internal/service/analyzer.go` | 四路 provider errgroup 并发化 |
| `internal/providers/spellbook/client.go` | `lastFetch` 竞争修复（锁内预留槽位、锁外等待） |
| `internal/service/analyzer_test.go` | 并发行为测试（barrier fakes，串行必红） |
| `cmd/*/web/app.js` | 淡入修复、默认收起、renderHealth、scrollToSlowly |
| `cmd/*/web/styles.css` | 纸片层阴影体系（L1 硬偏移规范） |
| `cmd/*/web/index.html` | 徽章结构、按钮箭头 ↓ |
| `resourse/graphic-design-styles/` | 设计风格 skill（本地私有，gitignore） |

---

# 2026-08-28 续记

## 今日任务概述

1. **牌表解析器 Go fuzz 测试**，fuzz 实测挖出并修复 5 个往返 bug（`150ea2e`）
2. **「差一张」组合缺件建议**：Spellbook 近完成组合独立成区，含分类徽章与排序（`4ae63e2`）
3. **主将颜色身份过滤**：缺件超出主将身份的建议整条剔除（同 `4ae63e2`）

以上代码改动均已 push 至 `origin/main`。

## 一、解析器 fuzz 测试（150ea2e）

- `FuzzParsePlainText`：断言不 panic、主将/主牌区存在、CardCount≤1000、导出再解析等价；种子语料在普通 `go test` 里随跑，失败样本自动落 `testdata/fuzz/`。
- fuzz 挖出的 5 个真 bug：后缀剥离不幂等（`/* 2 */` 数量累加）、split 内层斜杠被误当分隔符、主将重复计入主牌、`3x 牌名` 的 x 必须紧贴数字、fold-then-strip 顺序导致的注释残留。
- 用户要求低内存跑法：`-parallel 2`（默认 20 worker 太占内存）。

## 二、「差一张」缺件建议 + 分类徽章（4ae63e2）

- 学 loopline 的思路：从 Spellbook 每张牌的查询结果里挑「缺 1–2 件」的组合，单独成区「差一张就成组合」，按缺件数升序、制胜类优先排序；完整组合不再重复出现（`buildCombos` 只保留全 owned 的组合）。
- API 挂 `combo_suggestions`：owned/missing 卡图、成组合后结果、来源链接；前端徽章纯文字（制胜/无限掉血/无限法术力/额外回合/无限磨牌/组合产物），按用户反馈去掉了 emoji。
- `cmd/mobile/web` 镜像副本需手动 `cp` 三件套同步（这次差点漏掉）。

## 三、主将颜色身份过滤（4ae63e2）

- 问题：Spellbook 会因套牌里的中性牌（Sol Ring 之类）冒出主将永远不能合法使用的异色组合，loopline 自己也不做这个过滤。
- 方案：over-fetch 24 条 → catalog 批量查缺件色标 → **任一缺件**超出主将身份则整条剔除（组合需要每个部件，缺件查不到数据则放行）→ 截回 12 条；无色主将或主将数据缺失时跳过过滤（fail-open）。
- 踩坑：`swap.go` 既有同名 `commanderColorIdentity`（签名不同），新写的撞名编译必炸——删掉重复实现复用 swap 的严格版。
- E2E：Sram 纯白测试组，`Basalt Monolith + Power Artifact`{U} 被正确剔除，存活的 2 条缺件均在身份内。

## 四、今日相关文件

| 文件 | 说明 |
|---|---|
| `internal/deck/parse*.go`、`testdata/fuzz/` | fuzz 与 5 个解析修复 |
| `internal/service/combosuggest.go` + 测试 | 缺件建议核心逻辑、分类、身份过滤 |
| `internal/service/analyzer.go` / `model.go` | 接线：over-fetch→过滤→截断；`combo_suggestions` 字段 |
| `cmd/server/web/*` + `cmd/mobile/web/*` | 建议区块渲染、文字徽章、缺件虚线样式 |
---

# 2026-09-17 续记

## 今日任务概述

1. **接口契约化（二期）落地**：OpenAPI spec 单一事实源 + 三道机器校验 + 前端类型生成
2. 顺带修掉：`sanitizeValue` 反射编码忽略 `omitempty` 的认知偏差（spec 按实际行为描述）

## 一、契约 spec（docs/api/openapi.yaml）

- OpenAPI 3.1.0 手写，11 端点 + 43 个 schema；每个端点 description 全量枚举错误 code；
  请求体 `additionalProperties: false` 对齐 Go 端 `DisallowUnknownFields`。
- 严格度分级（一期约定）：请求体严格 / 错误信封严格 / 200 响应列全字段但暂不禁额外字段。
- 头部「已知偏差」6 条：ENCODING_FAILED 走 text/plain、card 复用 swap 错误映射、
  sanitize NaN→0 与颜色枚举序列化、sanitize 忽略 omitempty、请求体大小限制、静态资源不在契约内。

## 二、三道机器校验

| 校验 | 手段 | 防什么 |
|---|---|---|
| Go `contract_test.go` | yaml.v3 解析 spec，路由表↔paths 双向比对 | 文档漂移 |
| pytest `test_contract.py` | fixtures 录制快照对 spec 逐字段校验 + 错误信封活校验（全离线，CI 可跑） | 实现漂移 |
| CI `npm run gen:api` + `git diff --exit-code` | openapi-typescript 再生成无 diff | 手改生成文件 |

- fixtures：`pytest -m network --update-fixtures` 联网录制 11 端点 200 响应到
  `tests/fixtures/`，录制时即时校验；CI 用快照离线复验。成功响应必须真打第三方源，
  CI 拿不到，所以走「本地录制 + 离线复验」两段式。

## 三、关键发现：sanitize 忽略 omitempty

- 首次联网校验当场抓住：`results.edhpowerlevel.error` 是 `null` 而非缺省——
  `sanitizeValue` 反射编码输出结构体全部字段，不认 `omitempty`。
- 实测口径：空切片→`[]`、空映射→`{}`、空串→`""`、nil 指针→`null`（全响应仅
  `ProviderResult.error` 和 `Analysis` 的 construction_report/manabase/health 四个指针可空）。
- 决策：spec 按实际行为描述（所有 200 属性 required，指针字段 anyOf 可空），
  不动运行时行为；日后若让 sanitize 尊重 omitempty 再同步改 spec。

## 四、前端类型

- `npm run gen:api` → `cmd/server/web/api-types.d.ts`（910 行），app.js 顶部 JSDoc
  `@typedef` 引入，`render` / `renderHealth` / `renderManabase` / `renderCommanderPreview`
  等处加了示范标注；`tsc --checkJs` 全量扫了一遍，我新引入的联合类型收窄问题当场修掉
  （`resolveCommanderPreview` 返回改扁平可选字段，空名守卫从 `return null` 改 `return {}`，
  调用方从抛错进 catch 变优雅降级）。DOM narrowing 的历史噪音不在此期处理。

## 五、CI（.github/workflows/ci.yml）

- pip 加 `pyyaml jsonschema`；新增 setup-node 22 + `npm ci` + 类型再生成 diff 检查。
- pytest 步骤现在即契约测试步骤（离线含 fixtures 复验与错误信封校验）。

## 六、今日相关文件

| 文件 | 说明 |
|---|---|
| `docs/api/openapi.yaml` | 契约单一事实源（含已知偏差与工作流说明） |
| `internal/api/contract_test.go` | 路由表↔spec 同步测试 |
| `tests/test_contract.py` + `fixtures/` + conftest/helpers | pytest 契约层 |
| `package.json` + `cmd/{server,mobile}/web/api-types.d.ts` | 类型生成与镜像同步 |
| `tests/README.md` / `docs/api-architecture.md` | 契约工作流文档与二期状态 |

---

# 2026-09-17 续记（二）：内置缓存代理层

起因：产品不只我们自用——客户下载客户端后走同样的 Scryfall/EDHREC 网络路径，
卡图和卡牌资料经常出问题。讨论后确认正向代理无解（代理走的还是同一条坏路），
有价值的形态是"取数带韧性层"。全部 Go 原生标准库实现：

## 一、卡牌磁盘持久缓存（cardcatalog/diskcache.go）

- 内存缓存（24h TTL）之下再落磁盘：`CACHE_DIR`（默认 OS 用户缓存目录，安卓走
  TMPDIR 回退），每键一个 JSON 文件，SHA-256 文件名（规避 unicode 和 `Fire // Ice` 的斜杠），
  临时文件 + 原子改名写入（Windows 需先删目标）。
- 语义：新鲜磁盘条目直接服务并回填内存；过期条目在本轮调用里留作"断网保险"——
  上游故障时回填结果而不是报错（stale-on-error）。分析器的 DeckCards 依赖
  Lookup 不报错，所以断网时已缓存的牌表依然能出完整分析。
- 只有"一个名字连旧数据都没有"时错误才上浮，部分成功部分失败照旧 warnings 语义。

## 二、上游重试退避（httpretry.go）

- `doWithRetry`：3 次尝试、500ms/1s 退避、遵守 `Retry-After`（封顶 5s）、ctx 限总时长。
- POST 体逐次重建（reader 不能复用）；429/5xx 才重试，404 是答案不是故障。
- collection 批查 / search 分页 / autocomplete 三处调用点统一收口，UA 提为常量。

## 三、卡图代理路由（internal/api/images.go，GET /img/...）

- 前端 `proxiedImage()` 把 `cards.scryfall.io` 统一重写到本服务（中央两个 helper
  + renderCard + 双面切换共五处收口），三端共用同一前端，相对路径天然可用。
- 服务端：磁盘缓存（`CACHE_DIR/images`）+ 一次重试 + 断网回退旧图；
  `Cache-Control: immutable` 吃满浏览器缓存。
- 两个坑：CDN 的 `?<version>` 参数必须参与缓存键（否则换图被旧图永久挡住）；
  `filepath.Ext` 会把 `.jpg?123` 整个当扩展名，Windows 文件名禁 `?`，写入静默失败
  ——单测当场抓住。路径白名单校验（段字符集 + 扩展名），非法路径 400 标准信封。

## 四、配置与接线

- `CACHE_DIR` / `UPSTREAM_PROXY` / `SCRYFALL_IMAGE_URL` 三个新配置；
  `ProxyFunc()` 让客户端一键把全部上游流量指向本地加速器，不必改 shell 环境变量。
- /img 不进契约路由表（传输层基础设施），spec 头部注记 7 说明。

## 五、验证与单测

- Go 单测（httptest 假上游）：跨重启磁盘持久、过期条目断网降级、部分失败错误上浮、
  重试计数（两次 503 后第三次成功 / 耗尽报错）、retryDelay 纯函数表；
  /img 取缓存回源、缓存命中不回源、版本参数重取、404 不重试、路径校验白名单。
- pytest 联网补充：/img 非法路径 400 信封、经真实 /api/v1/card 的卡图 URL 走 /img 双次 200。
- Playwright 实测：分析一副牌后 6 个图片请求全部走 /img，0 个直连 scryfall，0 JS 报错。
