# 调参地图（tuning map）

手动调整评分口径、词表、目标值时，按这张表找到对应位置。改完必做的三件事：

1. `go test ./...`——特别留意 `TestReferenceDecks`（参照牌组金测），
   每副参照牌哪个指标动了会逐条打印；
2. 前端可调项改完跑 `bash scripts/sync-web.sh` 同步移动端；
3. 如果改动会影响用户分数（公式、词表、目标值），把
   `internal/service/analyzer.go` 的 `ScoringVersion` +1——分析缓存立即失效，
   用户不用等 TTL 过期就能看到新口径。

## Go 侧

| 可调项 | 位置 | 调整效果 |
|---|---|---|
| 构筑指标目标值（正向法力 38 等） | `internal/service/construction/tables.go` `targets` | 进度条显示 + 三选一缺口加权权重；改前端的 `config.js` 也要同步（有镜像测试把关） |
| 构筑指标分组与口径说明 | `construction/tables.go` `metricGroups` / `targets` 的 `group`、`hint` | 结果页"靠什么赢/怎么运转/怎么应对/牌组主线"分块与每张卡下的一句话口径；分组只影响呈现，不影响判定与缺口 |
| 胜负手短语表 | `construction/tables.go` `winconTextPhrases` | 命中即计胜负手；只收"规则上直接终局"的表述 |
| 穿透关键词表 | `construction/tables.go` `evasionKeywords` | evasive/finisher 判定 |
| 早二卡组合法术力阈值 | `internal/providers/edhpowerlevel/score.go` `earlyComboManaThreshold`（当前 7） | bracket 4 的"早二卡组合"触发线 |
| 组合终结向量词表 | `score.go` `gameEndingFeatureKeywords` | 命中才计入早/晚二卡组合；纯价值引擎（Redcap+Mimic）靠这个排除 |
| 加速的 Scryfall 查询串 / 回填上限 | `internal/service/build.go` `gapTypeQueries`、`buildBackfillLimit` | 组牌池耗尽后的回源补卡行为 |
| 常见单卡池（含色组） | `internal/service/staples.go` `staplePool` | 常见单卡面板；色组只是预过滤，真实色组会二次校验 |
| getcards 缓存 TTL | `internal/providers/edhpowerlevel/score.go` `getcardsTTL`（7 天） | 评分数据的陈旧容忍度 |
| 分析缓存 TTL | `internal/config/config.go`（env `CACHE_TTL` 等） | 结果缓存时长 |

## 前端（cmd/server/web，改完跑 sync-web.sh 同步移动端）

| 可调项 | 位置 | 调整效果 |
|---|---|---|
| 组牌目标张数 / 构筑指标目标值 / Combo 分类关键词 | `config.js`（`window.PL_CONFIG`） | 组牌器侧栏与 Combo 分类的展示口径 |
| 组牌分类口径（lands/ramp/single… 的文本判定） | `app.js` `builderCardMatches` | 侧栏实时计数；**是 Go `construction.Classify` 的镜像，改时两边同步** |
| 滚动显现动画时长/位移/缓动 | `styles.css` `.scroll-reveal` 区 | 结果页滚动手感；触屏与 reduced-motion 已全局禁用 |

## 判例口径（改动前先读）

分类的边界判例在 `internal/service/construction/report.go` 的 `classify` 注释里，
源头是对齐 CommanderSalt 2026.4195 的校准：

- 一发点伤（闪电击）不是胜负手；随场面放大的群体放血（灰衣怨灵/Blood Artist 形状）是；
- 一换一换地（Crop Rotation）不是加速；一换多（Harrow）是；
- "any target" 点伤算单解，"each creature" 全表伤害不算；
- 锁定光环（Pacifism）与夺获（Control Magic）算单卡解。

词表有增删时，优先把它们加进 `tables.go` 的表而不是再写散落的 if。
