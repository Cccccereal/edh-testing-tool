// 场景③：并发去重正确性——同 key 并发应合并为一次回源（singleflight 语义）。
//
// 50 个 VU 各发 1 次同一冷 key 的请求（setup 已重置 stub 计数；源延迟画像把
// 编排拉长到秒级，保证 50 个请求都落进同一个在途 flight）。
//
// 通过判据：
//   - miss 恰好 1 次（执行编排的那个调用），其余全部 coalesce
//   - 全部 200、deck_revision 非空（一致性由 harness 对照 stub 计数复核）
//
// 运行：k6 run -e BASE_URL=... -e DECK_ID=perf-<stamp> -e VUS=50 singleflight-dedup.k6.js

import { check } from 'k6';
import { Counter } from 'k6/metrics';

import { analyzeURL, cacheState } from './k6-lib.js';

const DECK_URL = `https://moxfield.com/decks/${__ENV.DECK_ID || 'deduptest01'}`;

const cacheCounts = new Counter('analyze_cache_state');

export const options = {
  summaryTrendStats: ['avg', 'min', 'med', 'max', 'p(90)', 'p(95)', 'p(99)'],
  scenarios: {
    burst: {
      executor: 'per-vu-iterations',
      vus: parseInt(__ENV.VUS || '50', 10),
      iterations: 1,
      gracefulStop: '30s',
    },
  },
  thresholds: {
    'http_req_failed': ['rate<0.01'],
    'checks': ['rate>0.99'],
    // 同 key 并发合并：整个突发只能有 1 次 miss（执行编排的那个调用）
    'analyze_cache_state{state:miss}': ['count==1'],
    // 50 个并发应大量合并进在途 flight；VU 启动抖动允许极少数落成 hit
    'analyze_cache_state{state:coalesce}': ['count>0'],
    'analyze_cache_state{state:hit}': ['count<5'],
    'analyze_cache_state{state:unknown}': ['count==0'],
  },
};

export default function () {
  const res = analyzeURL(DECK_URL);
  const state = cacheState(res);
  cacheCounts.add(1, { state });

  check(res, {
    'analyze 200': (r) => r.status === 200,
    '合并或首飞，不允许已命中（冷 key）': (r) => ['miss', 'coalesce'].includes(cacheState(r)),
    'deck_revision 非空': (r) => r.status === 200 && !!r.json('deck_revision'),
  });
}
