# 接口压测（k6）

《牌组平台测试体系规划.md》第三节的落地实现：三个压测场景、明确的指标与
通过判据、可复跑的编排器与基线回归闸门。所有场景的上游都指向
`tests/upstream_stub.py`（六源本地 stub，一源一端口），**绝不触达真实第三方**，
因此可以放心加压，且结果确定性可复现。

## 安装与运行

```powershell
winget install GrafanaLabs.k6        # 一次性安装（或 choco/scoop）
python perf/harness.py               # 三场景全跑 + 与 perf/baseline.json 对比
python perf/harness.py --scenario cache            # 单场景：cold | cache | dedup | all
python perf/harness.py --scenario cold --fault     # 冷启动 + salt 源超时（软降级画像）
python perf/harness.py --update-baseline           # 用本次结果刷新基线
```

harness 自动完成：编译并启动被测服务（六源指向 stub 池、独立临时 CACHE_DIR、
`SPELLBOOK_MIN_NAME_DELAY=10ms`）→ 按场景注入延迟画像 → 跑 k6（阈值即闸门，
退出码非 0 即失败）→ 采样服务进程 RSS → 落盘 `perf/results/` → 基线对比。
前置条件只有两个：PATH 上有 `k6` 与 `go`。

## 三个场景与通过判据

| 场景 | 脚本 | 度量 | 通过判据 |
|---|---|---|---|
| ① 冷启动全源编排 | `cold-orchestration.k6.js` | RPS、P50/P95/P99、partial 率 | 唯一 deck id 强制冷路径；错误率 <2%、partial 率 <5%（对齐规划书）；P95 回归交基线闸门 |
| ①b 软降级画像 | 同上 + `--fault` | partial 率、可用性 | salt 延迟超过收缩的 PROVIDER_TIMEOUT → partial 率 >90% 且响应仍 200（分源超时不丢可用性） |
| ② 缓存命中 vs 冷查询 | `cache-hit-cold.k6.js` | 冷/热时延分组（按 X-Cache 打 tag） | 热 P95 < 800ms（规划书判据）；收益倍数 = 冷时延 / 热 P95，实测约 **137×**（757ms → 5.5ms） |
| ③ 并发去重正确性 | `singleflight-dedup.k6.js` | X-Cache 分布、stub 上游计数 | 50 并发同 key：**恰好 1 次 miss + 49 次 coalesce**；stub 侧 moxfield/salt 各恰好 1 次调用（singleflight 语义） |

被测服务在 analyze 响应上带 `X-Cache: miss|coalesce|hit`（见
`docs/api/openapi.yaml`），这是场景②③的度量基础。

## 指标定义

- **RPS**：k6 `http_reqs.rate`（analyze 端点每秒完成数）；
- **P95/P99**：`http_req_duration`（分位数由 `summaryTrendStats` 显式导出）；
- **错误率**：非 2xx 占比（`http_req_failed`）；
- **partial 率**：响应体 `status=="partial"` 占比（`analyze_partial_rate`）——
  分源失败被软降级的比例，软降级本身是设计行为，判据只卡上限；
- **进程内存**：harness 采样被测服务 RSS 峰值（缓存膨胀监控），落进结果 JSON。

## 基线与回归闸门

`perf/baseline.json` 记录各场景的 P95/RPS 基线（本机首跑生成并提交）。
默认对比模式：P95 比基线慢超过 **25%** 或 RPS 掉超过 25% 即失败
（`--tolerance` 可调）。共享 runner（CI）噪声大，nightly 用同一闸门但
结果 artifact 一并上传供人工复核。基线漂移的合法来源（换机器、改画像）
用 `--update-baseline` 刷新并写明 `recorded_at`。

## 结果产物

- `perf/results/<场景>-<时间戳>.json`：k6 `--summary-export` 原始导出；
- `perf/results/run-<时间戳>.json`：harness 汇总（三场景指标 + RSS 峰值 + 参数）。

注意：k6 v2.2 的 summary-export 里 thresholds 布尔有反转 bug，**闸门以 k6
退出码为准**，harness 不读取那些布尔值。
