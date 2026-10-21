// k6 公共库：被测服务地址、analyze 请求封装、X-Cache 提取、场景通用指标。
// BASE_URL 由 perf/harness.py 通过 -e 注入（被测服务六源全部指向本地 stub）。

import http from 'k6/http';

export const BASE_URL = __ENV.BASE_URL || 'http://127.0.0.1:18781';

export function analyzeURL(url, tags = {}) {
  return http.post(`${BASE_URL}/api/v1/analyze`, JSON.stringify({ url }), {
    headers: { 'Content-Type': 'application/json' },
    tags: Object.assign({ endpoint: 'analyze' }, tags),
  });
}

export function analyzeDecklist(decklist, tags = {}) {
  return http.post(`${BASE_URL}/api/v1/analyze`, JSON.stringify({ decklist }), {
    headers: { 'Content-Type': 'application/json' },
    tags: Object.assign({ endpoint: 'analyze' }, tags),
  });
}

// X-Cache 响应头：miss（真实回源编排）/ coalesce（singleflight 在途合并）/ hit（缓存）
export function cacheState(res) {
  return (res.headers && (res.headers['X-Cache'] || res.headers['x-cache'])) || 'unknown';
}

// 每次迭代生成唯一的 deck id：URL 模式下缓存键 = sourceID，
// 唯一 id 保证每个请求都走完整的六源冷编排。
export function uniqueDeckURL(vu, iter) {
  return `https://moxfield.com/decks/perf${__ENV.RUN_STAMP || ''}-${vu}-${iter}`;
}
