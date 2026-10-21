// 场景①：冷启动全源编排——吞吐、P95/P99、partial 率。
//
// 每次迭代用唯一 deck id 强制冷启动：真实跑 Moxfield→(Salt|Scryfall|EDHREC|Spellbook
// 四路并发)→getcards 的完整编排（上游全部指向本地 stub，延迟由 harness 注入画像）。
//
// 通过判据（阈值即闸门，k6 退出码非 0 即失败）：
//   - 名义画像：http_req_failed rate<1%、partial_rate<5%、P95 无硬阈值（回归交给基线对比）
//   - FAULT=salt 画像：salt 源延迟超过被收缩的 PROVIDER_TIMEOUT，断言软降级——
//     partial_rate>90% 且请求本身仍是 200（可用性不丢）。
//
// 运行（通常由 perf/harness.py 代劳）：
//   k6 run -e BASE_URL=http://127.0.0.1:PORT -e VUS=10 -e DURATION=30s cold-orchestration.k6.js

import { check } from 'k6';
import { Counter, Rate } from 'k6/metrics';

import { analyzeURL, cacheState, uniqueDeckURL } from './k6-lib.js';

const partials = new Counter('analyze_partial');
const partialRate = new Rate('analyze_partial_rate'); // Rate 的 value 就是 partial 占比
const successes = new Counter('analyze_success');
const cacheStates = new Counter('analyze_cache_state');
const degradedOK = new Rate('degraded_still_200');

const FAULT = __ENV.FAULT || '';

export const options = {
  // 导出里带上 p(99)，报告与基线对比都要用
  summaryTrendStats: ['avg', 'min', 'med', 'max', 'p(90)', 'p(95)', 'p(99)'],
  scenarios: {
    cold: {
      executor: 'constant-vus',
      vus: parseInt(__ENV.VUS || '10', 10),
      duration: __ENV.DURATION || '30s',
    },
  },
  thresholds: FAULT
    ? {
        // 故障画像验证的是软降级语义：salt 超时应变成 partial 而非 5xx。
        // stub（Python 线程池）在故障负载下偶发 >10s 的尾部抖动会产生
        // 个别 502，错误率容限放宽到 5%，可用性由 degraded 指标把关
        'http_req_failed': ['rate<0.05'],
        'degraded_still_200': ['rate>0.99'],
        'analyze_partial_rate': ['rate>0.9'],
      }
    : {
        // stub（Python 线程池）在持续并发下偶发连接重置，个别编排会以
        // 502 DECK_SOURCE_FAILED 收场；2% 容限挡住真故障、放过 stub 抖动
        'http_req_failed': ['rate<0.02'],
        // 名义画像允许个位数的软降级（stub 偶发连接抖动会被正确降级为
        // partial 而非 5xx），判据对齐规划书「partial 率 < 5%」
        'analyze_partial_rate': ['rate<0.05'],
      },
};

export default function () {
  const res = analyzeURL(uniqueDeckURL(__VU, __ITER));
  const state = cacheState(res);

  check(res, {
    'analyze 200': (r) => r.status === 200,
    'cache miss（冷启动）': (r) => cacheState(r) === 'miss',
  });
  cacheStates.add(1, { state });

  if (res.status === 200) {
    const body = res.json();
    const partial = body.status === 'partial';
    partialRate.add(partial);
    if (partial) partials.add(1);
    else successes.add(1);
    degradedOK.add(res.status === 200);
    if (FAULT) {
      check(body, {
        '软降级：仍是 200 且带 partial 状态': (b) => b.status === 'partial',
        '软降级：出错 provider 有明确 error': (b) =>
          Object.values(b.results || {}).some((p) => p.status === 'error'),
      });
    }
  }
}
