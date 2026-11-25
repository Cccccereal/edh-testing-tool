# 金标准语料评测（把「主管觉得不准」变成秩相关系数）

「牌组强度评估」天然主观，所以**工具准不准**这件事不能用感觉闭环，必须外化基准：

1. 建 30–50 套**真实牌组**的语料库；
2. 让客户主管做**排序标注**（人对「从强到弱排序」的判断远比「这套牌几分」可靠）；
3. 每次调整评分口径后，算**工具分与主管排序的 Spearman 秩相关系数 ρ**；
4. ρ 不回退（最好上升）才允许合并，分数变化的牌组逐条列出来人工过目。

主观感知永远可以作为问题的来源，但不能作为改动是否正确的判据。配套的另两条线：
性能看 `perf/`（k6 基线），功能正确性看 `tests/`（pytest 合同测试）。

## 目录

```
eval/
  evaluate.py        采集 + 评测 + 基线对比（纯标准库，无第三方依赖）
  baseline.json      评分基线（git 跟踪，调参前后对比的锚点；首次 --save-baseline 生成）
  corpus/
    decks.csv        语料清单 + 主管标注表（Excel 可直接编辑，utf-8 with BOM）
    decks/<id>.txt   牌表快照（Moxfield 首采后自动落地，之后不再依赖外链）
  results/           评分快照与工作表（git 忽略，仅本机）
```

## 快速开始

```bash
# 0. 起服务
go run ./cmd/server          # 默认 http://localhost:18781

# 1. 往 eval/corpus/decks.csv 加真实牌组（source_url 填 Moxfield 链接）
# 2. 采集：脚本经服务端解析牌表，并把牌表快照存进 corpus/decks/<id>.txt
python eval/evaluate.py --server http://localhost:18781

# 3. 打印打乱的标注工作表给主管（只显示名称与主将，不给分数，避免锚定）
python eval/evaluate.py --worksheet
#    主管按强度排 1..N（1 = 最强），抄回 decks.csv 的 supervisor_rank 列（Excel 直接编辑）

# 4. 重新评测（可离线）
python eval/evaluate.py --scores eval/results/latest.json
```

## 常用命令

| 命令 | 用途 |
|---|---|
| `--server <url>` | 采集全部语料的评分并立即评测 |
| `--collect-only` | 只刷新评分快照（语料刚加了几套牌时） |
| `--scores <file>` | 离线评测既有快照，不打服务端 |
| `--metric <口径>` | 换评分口径评测，默认 `health`（工具自有健康分）。可选 `commandersalt.salt`、`edhpowerlevel.power_level` 等 |
| `--save-baseline` | 把本次评测存为基线（记录 git commit、ρ、各牌组分数） |
| `--fail-on-drop 0.05` | 与基线比 ρ 回退超过 0.05 → 退出码 1（CI 门禁用） |
| `--worksheet` | 打印主管排序工作表（顺序已打乱） |
| `--selftest` | 统计函数自检 |

## 语料库建设规范

- **来源**：客户真实在分析/争执的牌组优先，其次覆盖典型区间（1–4 各 bracket 至少 5 套）。
  语料越贴近真实使用场景，ρ 的说服力越强。
- **落地**：`decks.csv` 一行一套牌，`source_url` 只在首采时用一次，之后回归全部读
  `decks/<id>.txt` 快照——外链改版/删除不影响历史可比性。牌表变了就视为新牌组，加新行。
- **规模**：先 10 套跑通流程，再扩到 30–50 套。少于 10 套时 ρ 波动大，只看趋势不看绝对值。
- **`example-pipeline`**：管道验证条目（无主管排序，自动不参与统计），正式使用前删除。

## 调参回归循环（每次动评分口径必走）

对应 `docs/tuning.md` 的调参地图，改完四件事：

```bash
go test ./...                                        # 金测/单测不红
python eval/evaluate.py --server http://localhost:18781 --save-baseline   # 改动前：存基线（或直接用 git 里的 baseline.json 当基线）
# ... 改词表/目标值/权重 ...
python eval/evaluate.py --server http://localhost:18781                   # 改动后：复测
```

- **ρ 升**：改动有效，`--save-baseline` 更新基线，提交。
- **ρ 平但个别牌分变**：看输出的「分数变化牌组」清单，逐条判断是否伤到无辜。
- **ρ 降**：先看「不一致对」清单定位是哪几套牌失序，再决定回滚或收窄改动。
- 前端镜像口径（`app.js` `builderCardMatches`）改动同理：改完跑一次评测确认展示口径没漂。

## 输出怎么读

- **Spearman ρ**：−1 到 1。≥0.85 对齐很好；0.7–0.85 可接受；<0.7 说明口径与主管直觉系统性偏离。
- **Kendall τ-b**：逐对一致率视角，牌组少（<15 套）时比 ρ 更稳，两者趋势应一致。
- **不一致对**：主管认为 A 强于 B、工具却给 A 更低分的牌对，按分差降序排列——
  这是下一步调参的工作清单，也是与主管沟通的最好材料（「您把 A 排在 B 前，主要因为什么？」）。
- **换口径对比**：`--metric commandersalt.salt` vs `--metric health` 哪个 ρ 高，
  直接回答「我们的分比 CommanderSalt 更贴主管吗」——这是向客户证明工具价值的数字。

## 决策日志（配套纪律）

每次影响评分的改动，在 `docs/decisions.md`（自建）追加一条：

```
## 2026-11-02 胜负手短语表增补 X
- 症状：主管认为 A 应强于 B，但 health(A)=58 < health(B)=63（语料 #12）
- 证据：评测不一致对 #1，ρ=0.74
- 假设：X 在 CommanderSalt 2026.4195 口径下计入 wincon，我们的词表漏收
- 改动：tables.go winconTextPhrases +2 条；ScoringVersion +1
- 复测：ρ 0.74 → 0.79，不一致对 9 → 6；go test 全绿
- 结论：保留
```
