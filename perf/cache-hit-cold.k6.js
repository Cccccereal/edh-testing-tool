// 场景②：缓存命中 vs 冷查询——同 key 的冷→热收益曲线。
//
// setup() 先发一次冷查询（miss，真实编排），随后全部迭代打同一个 deck id：
// 每个响应按 X-Cache 打 tag，冷/热时延分组度量。TTL 内除首响外应全部为 hit。
//
// 通过判据：
//   - http_req_duration{cache:hit} P95 < 800ms（缓存命中的服务层开销上限，
//     对应规划书「P95 < 800ms」判据；冷查询无硬阈值，收益对比交给报告）
//   - hit 之外只允许出现 1 次 miss（setup 的那次），不允许 unknown
//
// 运行：k6 run -e BASE_URL=... -e DECK_ID=perf-<stamp> -e VUS=20 -e DURATION=30s cache-hit-cold.k6.js

import { check } from 'k6';
import { Counter, Trend } from 'k6/metrics';

import { analyzeURL, cacheState } from './k6-lib.js';

const DECK_URL = `https://moxfield.com/decks/${__ENV.DECK_ID || 'cachetest01'}`;

const coldDuration = new Trend('analyze_cold_duration', true);
const cacheCounts = new Counter('analyze_cache_state');

export const options = {
  summaryTrendStats: ['avg', 'min', 'med', 'max', 'p(90)', 'p(95)', 'p(99)'],
  scenarios: {
    warm: {
      executor: 'constant-vus',
      vus: parseInt(__ENV.VUS || '20', 10),
      duration: __ENV.DURATION || '30s',
    },
  },
  thresholds: {
    'http_req_failed': ['rate<0.01'],
    // 请求侧按「该走缓存」打 warm 标签（命中与否响应后才知道，tag 只能事前打），
    // 命中状态由 checks 严格把关：任何一次非 hit 都会挂闸门
    'http_req_duration{cache:warm}': ['p(95)<800'],
    'checks': ['rate>0.99'],
    // 让状态计数物化进导出，同时充当「至少命中过 / 不允许 unknown」闸门
    'analyze_cache_state{state:hit}': ['count>0'],
    'analyze_cache_state{state:unknown}': ['count==0'],
  },
};

export function setup() {
  // 冷查询：真实编排一次，把结果灌进 TTL/LRU 缓存，并记录冷时延作收益基线
  const res = analyzeURL(DECK_URL, { cache: 'miss' });
  check(res, { '冷查询 200': (r) => r.status === 200 });
  if (res.status === 200) {
    coldDuration.add(res.timings.duration, { cache: 'miss' });
    cacheCounts.add(1, { state: cacheState(res) });
  }
  return {};
}

export default function () {
  const res = analyzeURL(DECK_URL, { cache: 'warm' });
  const state = cacheState(res);
  cacheCounts.add(1, { state });

  check(res, {
    'analyze 200': (r) => r.status === 200,
    'TTL 内应命中缓存': (r) => cacheState(r) === 'hit',
  });
}
