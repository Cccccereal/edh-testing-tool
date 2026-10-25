// =============================================================================
// 手动调参区（tuning surface）
//
// 这里集中了前端所有"按产品判断调整"的数据调完刷新页面即生效（无需构建），
// 但注意与 Go 侧的口径约定：
//   - buildMetrics 的 target 必须与 internal/service/construction 的 targets
//     保持一致——侧栏进度条用这里的值显示，服务端缺口加权用 Go 侧的值计算，
//     两边漂移会让进度条误导（有镜像测试 frontend_mirror_test.go 把关）
//   - 分类口径（builder 的 lands/ramp/single 等）在 app.js 的
//     builderCardMatches，是 Go construction.Classify 的前端镜像，改时两边同步
// 更完整的调参地图见 docs/tuning.md
// =============================================================================
window.PL_CONFIG = Object.freeze({
  // 组牌器目标张数（主将 1 + 主板 99）
  buildTarget: 100,

  // 构筑指标目标值：组牌器侧栏进度条（id 顺序即显示顺序）
  buildMetrics: [
    { id: 'lands', label: '正向法力', target: 38 },
    { id: 'plan', label: '计划相关', target: 30 },
    { id: 'mass_interaction', label: '群体干扰', target: 6 },
    { id: 'single_interaction', label: '单体干扰', target: 12 },
    { id: 'draw_discard', label: '牌差件', target: 12 },
    { id: 'ramp', label: '加速', target: 10 }
  ],

  // Combo 分类：按 Spellbook 结果文本匹配的兜底关键词（fallback 为空时归 other）
  comboCategories: {
    win: { label: '制胜', fallback: ['win the game', 'opponents lose the game', 'target opponent loses the game'] },
    lifeloss: { label: '无限掉血', fallback: ['lifeloss'] },
    mana: { label: '无限法术力', fallback: ['mana'] },
    turns: { label: '额外回合', fallback: ['extra turn', 'infinite turn', 'near-infinite turn', 'additional combat', 'extra combat'] },
    mill: { label: '无限磨牌', fallback: ['mill'] },
    other: { label: '组合产物', fallback: [] }
  }
});
