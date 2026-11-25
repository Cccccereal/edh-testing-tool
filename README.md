# Commander Power Check

输入公开的 Moxfield Commander 牌组 URL 或粘贴牌表文本，同时查询 CommanderSalt 与 EDH Power Level 的评分，并给出构筑缺口推荐与一套引导式组牌工具。

## 功能

- **双评分独立展示**：CommanderSalt 与 EDH Power Level 分别给出 Bracket（规则值 / 评估值）与 Power Level，不计算平均值。EDH Power Level 判定还会列出 Game Changers、早期 2-Card Combo、Extra Turns、Mass Land Denial 的具体数量与依据。
- **构筑概览与缺口报告**：按正向法力、计划相关、群体干扰、单体干扰、牌差件、加速六类统计当前数量与目标缺口。
- **构筑缺口推荐**：只推荐能补足当前短缺类别的单卡，并说明每张卡的填补理由。
- **法术力基础分析**：基于 Frank Karsten 的地张数回归与条件超几何模型，按颜色拆解地牌供给是否够；法术力曲线旁直接列出该费用的牌，颜色分析六色并列。
- **引导式组牌器**：没有牌时输入主将名称开始，逐轮三选一，宽屏下三张候选以大图卡并排；支持一键出地、常用加速单卡与「可用的 Game Changer」快捷添加、快速加基本地、已选牌移除与导出。
- **单卡替换预览**：选择一张移除牌与一张加入牌，对比替换前后的构筑指标、牌张数与基础合法性，不修改 Moxfield、不保存版本。
- **结果页报头与常驻汇总条**：报头带主将原画、色组与健康分；评分滚出视口后，底部常驻牌名、类型构成、Bracket 与未达标的构筑指标；同一副牌表的健康分变化会提示「较上次」。
- **轻量牌表编辑器**：牌表按类型分组，提供文字行（数量 / 牌名 / 费用）与卡图两种视图，宽屏下侧边带固定的卡图预览栏；支持增删卡牌、撤销/重做、保存版本与导出牌表文本。
- **卡图与关联数据**：Scryfall 卡图（含双面牌正反面切换）、Commander Spellbook 组合、EDHREC 推荐；费用与规则文本里的法术力符号为自绘的内联 SVG。

## 给客户：直接下载客户端

客户只需要 GitHub Releases 页面里的一个文件：

- **`edh-powerlevel-client.exe`** —— 双击即用。它是 Tauri 外壳，内部已嵌入 Go 服务端与前端，首次启动会把自己释放到 `%LOCALAPPDATA%\EDHPowerLevel\runtime\` 并随机挑一个空闲端口运行，然后自动打开窗口。

不需要安装 Go、Rust、Node 或单独跑 `server.exe`。Releases 由推送 `main` 分支自动触发构建（版本号取最后一次提交的时间）。

### 使用前提

1. **联网且能访问海外服务**。评分依赖 Scryfall、CommanderSalt、EDH Power Level、EDHREC、Commander Spellbook、Moxfield 等外部站点，网络不通时相关评分会显示「跳过」。
2. **本机有 Chrome / Edge**。EDH Power Level 的评分复用本机浏览器内核抓取页面。
3. **Windows 系统建议带 WebView2 Runtime**。Tauri 窗口渲染依赖它；Win10/11 多数自带，精简系统或老系统需另行安装。

## 给开发者：本地运行

需要 Go 1.24+。首次会自动联网下载依赖，并自动查找本机的 Chrome / Chromium / Edge 做 EDH Power Level 评分。

```bash
go run ./cmd/server
```

打开 <http://localhost:18781>。

其他平台手动指定浏览器路径：

```bash
BROWSER_PATH=/usr/bin/chromium go run ./cmd/server
```

## 构建桌面客户端

单文件客户端通过 Tauri 构建，内置服务端二进制由 `include_bytes!` 编译进 exe：

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\build-client.ps1
```

需要本机装有 Rust（`cargo`）与 Go。产物在 `src-tauri\target\release\edh-powerlevel-client.exe`。CI 的 `.github/workflows/release.yml` 会自动完成同样的构建并发布到 Releases，正常交付不必在本地手动构建。

## 测试

```bash
go test ./...
go build ./cmd/server
```

单元测试使用本地模拟响应，不依赖第三方站点。实际端到端分析会受第三方网站可用性、页面改版及网络环境影响。项目不会绕过验证码或访问控制。
