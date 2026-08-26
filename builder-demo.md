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