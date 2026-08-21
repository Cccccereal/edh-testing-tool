const form = document.querySelector('#analyze-form');
const input = document.querySelector('#deck-url');
const decklistInput = document.querySelector('#decklist-input');
const submitButton = document.querySelector('#submit-button');
const message = document.querySelector('#form-message');
const loading = document.querySelector('#loading');
const results = document.querySelector('#results');
const warning = document.querySelector('#warning');
const retryButton = document.querySelector('#retry-button');
const decklistToggle = document.querySelector('#decklist-toggle');
const copyDecklistButton = document.querySelector('#copy-decklist');
const clearDecklistButton = document.querySelector('#clear-decklist');
const themeToggle = document.querySelector('#theme-toggle');
let currentDeckText = '';
let currentDeckCards = [];
let pendingSwapAdd = '';
let selectedSwapRemove = '';
const swapModal = document.querySelector('#swap-modal');
const swapSearch = document.querySelector('#swap-search');
const swapSubmit = document.querySelector('#swap-submit');
const swapMessage = document.querySelector('#swap-message');
const swapResult = document.querySelector('#swap-result');

// --- light deck editor state (pure front-end, direction A) ---------------------
// The editor works on a mutable copy of the analyzed deck. Every mutating action
// pushes the previous snapshot onto an undo stack (and clears redo); saved versions
// persist to localStorage keyed by the deck's source id.
const EDITOR_VERSIONS_KEY = 'powerlevel.deck.versions.v1';
let editorCards = [];           // mutable deck_cards array under edit
let editorUndo = [];            // snapshots before each applied action
let editorRedo = [];            // snapshots after each undone action
let editorDirty = false;        // true when a local edit has not been re-analyzed
const editorToolbar = document.querySelector('#editor-toolbar');
const editorAddInput = document.querySelector('#editor-add-card');
const editorAddButton = document.querySelector('#editor-add-submit');
const editorAddSuggestions = document.querySelector('#editor-add-suggestions');
const editorUndoButton = document.querySelector('#editor-undo');
const editorRedoButton = document.querySelector('#editor-redo');
const editorSaveVersionButton = document.querySelector('#editor-save-version');
const editorExportButton = document.querySelector('#editor-export');
const editorVersionsButton = document.querySelector('#editor-versions-toggle');
const editorVersionsPanel = document.querySelector('#editor-versions-panel');
const editorVersionsList = document.querySelector('#editor-versions-list');
const editorDirtyNote = document.querySelector('#editor-dirty-note');
let activeSourceId = '';

// --- guided deck builder state ---------------------------------------------
const buildEntryButton = document.querySelector('#build-entry-button');
const builder = document.querySelector('#builder');
const builderClose = document.querySelector('#builder-close');
const builderCommanderInput = document.querySelector('#builder-commander');
const builderCommanderSuggestions = document.querySelector('#builder-commander-suggestions');
const builderStartButton = document.querySelector('#builder-start');
const builderMessage = document.querySelector('#builder-message');
const builderWorkflow = document.querySelector('#builder-workflow');
const builderCandidates = document.querySelector('#builder-candidates');
const builderSkip = document.querySelector('#builder-skip');
const builderLandsButton = document.querySelector('#builder-lands');
const builderLandsPanel = document.querySelector('#builder-lands-panel');
const builderLandsGrid = document.querySelector('#builder-lands-grid');
const builderStaplesButton = document.querySelector('#builder-staples');
const builderStaplesPanel = document.querySelector('#builder-staples-panel');
const builderStaplesCategories = document.querySelector('#builder-staples-categories');
const builderSidebar = document.querySelector('#builder-sidebar');
const builderComplete = document.querySelector('#builder-complete');
const builderBackEdit = document.querySelector('#builder-back-edit');
const builderExport = document.querySelector('#builder-export');
const builderAnalyze = document.querySelector('#builder-analyze');
const builderRandomButton = document.querySelector('#builder-random');
const builderCommanderPreview = document.querySelector('#builder-commander-preview');
const builderPartnerRow = document.querySelector('#builder-partner-row');
const builderPartnerInput = document.querySelector('#builder-partner');
const builderPartnerSuggestions = document.querySelector('#builder-partner-suggestions');
const builderPartnerAddButton = document.querySelector('#builder-partner-add');

// The draft being built: commander name + already-chosen mainboard card names.
let buildCommander = '';
let buildCommanders = [];      // resolved commander objects { name, card?, isPartner }
let buildChosen = [];           // card names (lowercase) already added to the draft
let buildCards = [];            // { name, card? } resolved rows for export/analysis
let buildColors = [];           // commander color identity (for basic-land gating)
let buildCandidates = [];       // currently displayed 3 candidates
let recentShown = [];           // sliding window of names shown (not chosen) in the last two refreshes

const BASIC_LANDS = ['Plains', 'Island', 'Swamp', 'Mountain', 'Forest', 'Wastes'];
const BUILD_TARGET = 100;

// Canonical WUBRG order for the color-pip composition and symbol mapping. Symbols are
// Scryfall-style glyphs ({W} → "{W}"), so they render as recognizable mana letters.
const MANA_COLORS = ['W', 'U', 'B', 'R', 'G'];
function manaSymbolFor(color) {
  switch (color) {
    case 'W': return '{W}';
    case 'U': return '{U}';
    case 'B': return '{B}';
    case 'R': return '{R}';
    case 'G': return '{G}';
    default: return color;
  }
}

// A card (basic lands excepted) is a singleton: one copy at most in the Commander
// mainboard. We check the authoritative draft list, not the name-only `buildChosen`
// set, so a quick-add can't slip in a second copy that the export would silently
// collapse back down to one.
function isBasicLandName(name) {
  return BASIC_LANDS.includes(String(name || '').trim());
}

// True when the draft may legally receive another copy of `name`.
function canAddBuildCard(name) {
  if (!name) return false;
  if (isBasicLandName(name)) return true;
  return !buildCards.some((card) => normalizeBuildName(card.name) === normalizeBuildName(name));
}

// Surface a transient reason the add was refused, without clobbering the in-flight
// "加载中…" state of a running suggestion request.
let buildRejectTimer = 0;
function noteBuildReject(message) {
  builderMessage.textContent = message;
  if (buildRejectTimer) clearTimeout(buildRejectTimer);
  buildRejectTimer = setTimeout(() => {
    builderMessage.textContent = '';
    buildRejectTimer = 0;
  }, 2800);
}

// Render the resolved-commander thumbnail + name into the preview strip under the
// commander input. Shows the primary plus (for a partner pair) the partner name.
function renderCommanderPreview(commanders) {
  if (!Array.isArray(commanders) || commanders.length === 0) {
    builderCommanderPreview.hidden = true;
    builderCommanderPreview.innerHTML = '';
    return;
  }
  builderCommanderPreview.innerHTML = commanders.map((commander) => {
    const card = commander.card || {};
    const image = cardImage(card) || cardPreviewImage(card);
    const isPartner = commander.is_partner;
    return `
      <img loading="lazy" src="${escapeHTML(image)}" alt="${escapeHTML(commander.name)}" data-preview-src="${escapeHTML(cardPreviewImage(card) || image)}" data-preview-name="${escapeHTML(commander.name)}" data-card-text="${escapeHTML(previewTextFor(card))}">
      <div class="builder-commander-preview-body">
        <strong>${escapeHTML(commander.name)}</strong>
        ${isPartner ? '<small>可搭配 Partner / Friends Forever / 选择身世</small>' : ''}
      </div>`;
  }).join('');
  builderCommanderPreview.hidden = false;
}

async function resolveCommanderPreview(names) {
  if (!names.length) {
    renderCommanderPreview([]);
    return null;
  }
  try {
    const response = await fetch('/api/v1/resolve-commanders', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ commanders: names })
    });
    const payload = await response.json();
    if (!response.ok) {
      return { error: payload.error?.message || '无法解析主将。' };
    }
    return payload;
  } catch {
    return { error: '解析主将失败，请重试。' };
  }
}

// After the primary commander is resolved, reveal the partner input only when the
// commander actually carries a partner relationship.
function syncPartnerUI(commanders) {
  const primary = commanders && commanders[0];
  if (primary && primary.is_partner) {
    builderPartnerRow.hidden = false;
  } else {
    builderPartnerRow.hidden = true;
  }
}

async function pickRandomCommander() {
  builderRandomButton.disabled = true;
  builderRandomButton.textContent = '抽取中…';
  builderMessage.textContent = '';
  try {
    const response = await fetch('/api/v1/random-commander', { method: 'POST' });
    const payload = await response.json();
    if (!response.ok) {
      builderMessage.textContent = payload.error?.message || '无法随机抽主将。';
      return;
    }
    builderCommanderInput.value = payload.name || '';
    buildCommander = payload.name || '';
    buildCommanders = [{ name: payload.name, card: payload.card, is_partner: payload.is_partner }];
    renderCommanderPreview(buildCommanders);
    syncPartnerUI(buildCommanders);
  } catch {
    builderMessage.textContent = '随机抽主将失败，请重试。';
  } finally {
    builderRandomButton.disabled = false;
    builderRandomButton.textContent = '随机抽主将';
  }
}

async function addPartnerCommander() {
  const secondary = builderPartnerInput.value.trim();
  if (!secondary) {
    builderMessage.textContent = '请输入搭档主将名称。';
    return;
  }
  if (!buildCommanders.length) {
    builderMessage.textContent = '请先确定主将。';
    return;
  }
  builderMessage.textContent = '';
  builderPartnerAddButton.disabled = true;
  try {
    const result = await resolveCommanderPreview([buildCommanders[0].name, secondary]);
    if (result.error) {
      builderMessage.textContent = result.error;
      return;
    }
    const commanders = result.commanders || [];
    buildCommanders = commanders.map((c) => ({ name: c.name, card: c.card, is_partner: c.is_partner }));
    buildColors = (result.color_identity || []).slice();
    buildCommander = buildCommanders.map((c) => c.name).join(' // ');
    renderCommanderPreview(buildCommanders);
    renderBuilderSidebar();
    builderPartnerInput.value = '';
  } catch {
    builderMessage.textContent = '添加搭档失败，请重试。';
  } finally {
    builderPartnerAddButton.disabled = false;
  }
}

// 一键出地的地牌分类。ID 与后端 service.LandCategories 对齐；点单类后按主将色组
// 过滤，再渲染可加入草稿的地牌小图。
const LAND_CATEGORIES = [
  { id: 'shock', label: '电震' },
  { id: 'surveil', label: '刺探' },
  { id: 'original_dual', label: '老圈' },
  { id: 'verge', label: '边陲' },
  { id: 'scry', label: '占卜地' },
  { id: 'multiplayer', label: '多人地' },
  { id: 'fetch', label: '找地' },
  { id: 'triome', label: '三色圈' },
  { id: 'check', label: '检查地' },
  { id: 'reveal', label: '展示地' },
  { id: 'slow', label: '慢地' }
];

// 常见单卡分类。ID 与后端 service.StapleCategories 对齐；点单类后按主将色组过滤。
const STAPLE_CATEGORIES = [
  { id: 'ramp', label: '常见法术力增长' },
  { id: 'game-changer', label: '可用的 Game Changer' }
];

// construction metric targets (labels mirror the server's construction.Report).
const BUILD_METRICS = [
  { id: 'lands', label: '正向法力', target: 38 },
  { id: 'plan', label: '计划相关', target: 30 },
  { id: 'mass_interaction', label: '群体干扰', target: 6 },
  { id: 'single_interaction', label: '单体干扰', target: 12 },
  { id: 'draw_discard', label: '牌差件', target: 12 },
  { id: 'ramp', label: '加速', target: 10 }
];

function openBuilder() {
  builder.hidden = false;
  builderMessage.textContent = '';
  builderWorkflow.hidden = true;
  builderComplete.hidden = true;
  builderCommanderInput.value = '';
  buildCommander = '';
  buildCommanders = [];
  buildChosen = [];
  buildCards = [];
  buildColors = [];
  buildCandidates = [];
  recentShown = [];
  hasShownCompletePulse = false;
  builderCommanderPreview.hidden = true;
  builderCommanderPreview.innerHTML = '';
  builderPartnerRow.hidden = true;
  builderPartnerInput.value = '';
  builderCommanderInput.focus();
  builder.scrollIntoView({ behavior: 'smooth', block: 'start' });
}

function closeBuilder() {
  builder.hidden = true;
  hideCommanderSuggestions();
}

// --- commander autocomplete -------------------------------------------------
// Typeahead on the builder's commander field. Each keystroke debounces a query to
// the server, which filters Scryfall autocomplete down to cards legal as a
// Commander. Out-of-order responses are discarded via AbortController, and selected
// suggestions are simply written back into the input for the user to start with.
let commanderAutocompleteController = null;
let commanderAutocompleteTimer = null;
let commanderAutocompleteIndex = -1;

function commanderSuggestionItems() {
  return Array.from(builderCommanderSuggestions.querySelectorAll('[data-suggestion]'));
}

function renderCommanderSuggestions(names) {
  commanderAutocompleteIndex = -1;
  if (!names || names.length === 0) {
    hideCommanderSuggestions();
    return;
  }
  builderCommanderSuggestions.innerHTML = names.map((name) =>
    `<li role="option" data-suggestion="${escapeHTML(name)}">${escapeHTML(name)}</li>`).join('');
  builderCommanderSuggestions.hidden = false;
}

function hideCommanderSuggestions() {
  commanderAutocompleteController?.abort();
  builderCommanderSuggestions.hidden = true;
  builderCommanderSuggestions.innerHTML = '';
  commanderAutocompleteIndex = -1;
}

function highlightCommanderSuggestion(index) {
  const items = commanderSuggestionItems();
  items.forEach((item, i) => {
    item.classList.toggle('active', i === index);
    if (i === index) item.scrollIntoView({ block: 'nearest' });
  });
}

async function queryCommanderSuggestions(query) {
  const value = String(query ?? '').trim();
  if (value.length < 2) {
    hideCommanderSuggestions();
    return;
  }
  commanderAutocompleteController?.abort();
  const controller = new AbortController();
  commanderAutocompleteController = controller;
  try {
    const response = await fetch(`/api/v1/commander-autocomplete?q=${encodeURIComponent(value)}`, { signal: controller.signal });
    const payload = await response.json();
    if (response.ok && Array.isArray(payload.suggestions)) {
      renderCommanderSuggestions(payload.suggestions);
    } else {
      hideCommanderSuggestions();
    }
  } catch (error) {
    // Aborted requests land here intentionally; anything else is a transient miss.
    if (error.name !== 'AbortError') hideCommanderSuggestions();
  }
}

function chooseCommanderSuggestion(name) {
  builderCommanderInput.value = name;
  hideCommanderSuggestions();
  builderCommanderInput.focus();
}

builderCommanderInput.addEventListener('input', () => {
  clearTimeout(commanderAutocompleteTimer);
  commanderAutocompleteTimer = setTimeout(() => queryCommanderSuggestions(builderCommanderInput.value), 220);
});

builderCommanderInput.addEventListener('keydown', (event) => {
  const items = commanderSuggestionItems();
  if (event.key === 'ArrowDown') {
    event.preventDefault();
    commanderAutocompleteIndex = Math.min(commanderAutocompleteIndex + 1, items.length - 1);
    highlightCommanderSuggestion(commanderAutocompleteIndex);
  } else if (event.key === 'ArrowUp') {
    event.preventDefault();
    commanderAutocompleteIndex = Math.max(commanderAutocompleteIndex - 1, 0);
    highlightCommanderSuggestion(commanderAutocompleteIndex);
  } else if (event.key === 'Enter') {
    const active = items[commanderAutocompleteIndex];
    if (active) {
      event.preventDefault();
      chooseCommanderSuggestion(active.dataset.suggestion);
    }
  } else if (event.key === 'Escape') {
    hideCommanderSuggestions();
  }
});

builderCommanderInput.addEventListener('blur', () => {
  // Delay so a click on a suggestion lands before we tear the list down.
  setTimeout(hideCommanderSuggestions, 120);
});

builderCommanderSuggestions.addEventListener('mousedown', (event) => {
  const item = event.target.closest('[data-suggestion]');
  if (item) {
    event.preventDefault();
    chooseCommanderSuggestion(item.dataset.suggestion);
  }
});

// --- editor card autocomplete -------------------------------------------------
// Typeahead on the light editor's add-card input, reusing the same debounce +
// AbortController pattern as the commander field but against the (non-Commander
// filtered) card-autocomplete endpoint.
let editorAutocompleteController = null;
let editorAutocompleteTimer = null;
let editorAutocompleteIndex = -1;

function editorSuggestionItems() {
  return Array.from(editorAddSuggestions.querySelectorAll('[data-suggestion]'));
}

function renderEditorSuggestions(names) {
  editorAutocompleteIndex = -1;
  if (!names || names.length === 0) {
    hideEditorSuggestions();
    return;
  }
  editorAddSuggestions.innerHTML = names.map((name) =>
    `<li role="option" data-suggestion="${escapeHTML(name)}">${escapeHTML(name)}</li>`).join('');
  editorAddSuggestions.hidden = false;
}

function hideEditorSuggestions() {
  editorAutocompleteController?.abort();
  editorAddSuggestions.hidden = true;
  editorAddSuggestions.innerHTML = '';
  editorAutocompleteIndex = -1;
}

function highlightEditorSuggestion(index) {
  const items = editorSuggestionItems();
  items.forEach((item, i) => {
    item.classList.toggle('active', i === index);
    if (i === index) item.scrollIntoView({ block: 'nearest' });
  });
}

async function queryEditorSuggestions(query) {
  const value = String(query ?? '').trim();
  if (value.length < 2) {
    hideEditorSuggestions();
    return;
  }
  editorAutocompleteController?.abort();
  const controller = new AbortController();
  editorAutocompleteController = controller;
  try {
    const response = await fetch(`/api/v1/card-autocomplete?q=${encodeURIComponent(value)}`, { signal: controller.signal });
    const payload = await response.json();
    if (response.ok && Array.isArray(payload.suggestions)) {
      renderEditorSuggestions(payload.suggestions);
    } else {
      hideEditorSuggestions();
    }
  } catch (error) {
    if (error.name !== 'AbortError') hideEditorSuggestions();
  }
}

function chooseEditorSuggestion(name) {
  editorAddInput.value = name;
  hideEditorSuggestions();
  editorAddInput.focus();
}

editorAddInput.addEventListener('input', () => {
  clearTimeout(editorAutocompleteTimer);
  editorAutocompleteTimer = setTimeout(() => queryEditorSuggestions(editorAddInput.value), 220);
});

editorAddInput.addEventListener('keydown', (event) => {
  const items = editorSuggestionItems();
  if (event.key === 'ArrowDown') {
    event.preventDefault();
    editorAutocompleteIndex = Math.min(editorAutocompleteIndex + 1, items.length - 1);
    highlightEditorSuggestion(editorAutocompleteIndex);
  } else if (event.key === 'ArrowUp') {
    event.preventDefault();
    editorAutocompleteIndex = Math.max(editorAutocompleteIndex - 1, 0);
    highlightEditorSuggestion(editorAutocompleteIndex);
  } else if (event.key === 'Enter') {
    const active = items[editorAutocompleteIndex];
    if (active) {
      event.preventDefault();
      chooseEditorSuggestion(active.dataset.suggestion);
    }
  } else if (event.key === 'Escape') {
    hideEditorSuggestions();
  }
});

editorAddInput.addEventListener('blur', () => {
  setTimeout(hideEditorSuggestions, 120);
});

editorAddSuggestions.addEventListener('mousedown', (event) => {
  const item = event.target.closest('[data-suggestion]');
  if (item) {
    event.preventDefault();
    chooseEditorSuggestion(item.dataset.suggestion);
  }
});


// 一键出地：展开/收起八个地牌分类按钮。点击单个分类按主将色组请求可用地牌，
// 渲染成可点选的小图，点某张地把它加入草稿。再次点击已打开的分类会收起它。
function toggleLandsPanel() {
  const visible = !builderLandsPanel.hidden;
  if (visible) {
    builderLandsPanel.hidden = true;
    return;
  }
  renderCategories(builderLandsGrid, LAND_CATEGORIES, 'data-land-category');
  builderLandsPanel.hidden = false;
}

// 常见单卡：展开/收起分类按钮，点单个分类按主将色组请求可用单卡并渲染成可点选
// 的小图，点某张常见单卡把它加入草稿。再次点击已打开的分类会收起它。
function toggleStaplesPanel() {
  const visible = !builderStaplesPanel.hidden;
  if (visible) {
    builderStaplesPanel.hidden = true;
    return;
  }
  renderCategories(builderStaplesCategories, STAPLE_CATEGORIES, 'data-staple-category');
  builderStaplesPanel.hidden = false;
}
// Render a grid of category buttons into `grid`, tagging each with `attribute`. Any
// previously rendered result area is left untouched (each category's result is placed
// in its own dedicated result box, so reopening a category does not duplicate it).
function renderCategories(grid, categories, attribute) {
  grid.innerHTML = categories.map((category) => `
    <button type="button" class="builder-land-category" ${attribute}="${category.id}">
      <strong>${category.label}</strong>
    </button>`).join('');
}

function addStapleCard(name, card, gameChanger) {
  if (!name) return;
  const key = normalizeBuildName(name);
  if (buildChosen.includes(key)) return;
  if (!canAddBuildCard(name)) {
    noteBuildReject('"' + name + '" 是重复普通牌，只能放一张（基本地除外）。');
    return;
  }
  if (buildCards.length + (buildCommanders.length || (buildCommander ? 1 : 0)) >= BUILD_TARGET) return;
  buildChosen.push(key);
  const entry = { name, card: card || { name, type_line: 'Artifact' } };
  if (gameChanger) entry.game_changer = true;
  buildCards.push(entry);
  renderBuilderSidebar();
  if (isBuilderComplete()) {
    if (!hasShownCompletePulse) {
      hasShownCompletePulse = true;
      builderSidebar.classList.add('complete-pulse');
      setTimeout(() => builderSidebar.classList.remove('complete-pulse'), 2000);
    }
  } else {
    refreshIfCandidateCollides();
  }
}

async function loadStapleCategory(categoryID) {
  const resultBox = document.querySelector('#builder-staples-result');
  if (!resultBox) return;
  if (resultBox.dataset.activeCategory === categoryID && !resultBox.classList.contains('collapsed')) {
    resultBox.classList.add('collapsed');
    resultBox.innerHTML = '';
    delete resultBox.dataset.activeCategory;
    return;
  }
  resultBox.dataset.activeCategory = categoryID;
  const category = STAPLE_CATEGORIES.find((item) => item.id === categoryID);
  resultBox.classList.remove('collapsed');
  resultBox.innerHTML = '<p class="editor-empty">正在加载单卡…</p>';
  try {
    const response = await fetch('/api/v1/build-staples', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ category: categoryID, color_identity: buildColors })
    });
    const payload = await response.json();
    if (!response.ok) throw new Error(payload.error?.message || '无法加载单卡。');
    const staples = Array.isArray(payload.staples) ? payload.staples : [];
    if (!staples.length) {
      resultBox.innerHTML = '<p class="editor-empty">该类别在你的主将色组内没有可用单卡。</p>';
      return;
    }
    resultBox.innerHTML = `<div class="builder-lands-head"><strong>${escapeHTML(payload.category_label || category?.label || '')}</strong><small>点击加入草稿</small></div>
      <div class="builder-lands-cards">${staples.map((staple) => {
        const image = cardImage(staple.card);
        const card = staple.card || {};
        return `<button type="button" class="builder-land-card" data-staple-name="${escapeHTML(staple.name)}" data-staple-card="${escapeHTML(JSON.stringify(card))}" data-staple-gc="${staple.game_changer ? '1' : '0'}">${image ? `<img loading="lazy" src="${escapeHTML(image)}" alt="${escapeHTML(staple.name)}">` : '<div class="builder-candidate-placeholder"></div>'}<span>${escapeHTML(staple.name)}</span></button>`;
      }).join('')}</div>`;
  } catch (error) {
    resultBox.innerHTML = `<p class="form-message">${escapeHTML(error.message || '加载失败')}</p>`;
  }
}

async function loadLandCategory(categoryID) {
  const resultBox = document.querySelector('#builder-lands-result');
  if (!resultBox) return;
  if (resultBox.dataset.activeCategory === categoryID && !resultBox.classList.contains('collapsed')) {
    resultBox.classList.add('collapsed');
    resultBox.innerHTML = '';
    delete resultBox.dataset.activeCategory;
    return;
  }
  resultBox.dataset.activeCategory = categoryID;
  const category = LAND_CATEGORIES.find((item) => item.id === categoryID);
  resultBox.classList.remove('collapsed');
  resultBox.innerHTML = '<p class="editor-empty">正在加载地牌…</p>';
  try {
    const response = await fetch('/api/v1/build-lands', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ category: categoryID, color_identity: buildColors })
    });
    const payload = await response.json();
    if (!response.ok) throw new Error(payload.error?.message || '无法加载地牌。');
    const lands = Array.isArray(payload.lands) ? payload.lands : [];
    if (!lands.length) {
      resultBox.innerHTML = '<p class="editor-empty">该类别在你的主将色组内没有可用地牌。</p>';
      return;
    }
    resultBox.innerHTML = `<div class="builder-lands-head"><strong>${escapeHTML(payload.category_label || category?.label || '')}</strong><small>点击地牌加入草稿</small></div>
      <div class="builder-lands-cards">${lands.map((land) => {
        const image = cardImage(land.card);
        return `<button type="button" class="builder-land-card" data-land-name="${escapeHTML(land.name)}">${image ? `<img loading="lazy" src="${escapeHTML(image)}" alt="${escapeHTML(land.name)}">` : '<div class="builder-candidate-placeholder"></div>'}<span>${escapeHTML(land.name)}</span></button>`;
      }).join('')}</div>`;
  } catch (error) {
    resultBox.innerHTML = `<p class="form-message">${escapeHTML(error.message || '加载失败')}</p>`;
  }
}

function addLandCard(name) {
  if (!name) return;
  if (!canAddBuildCard(name)) {
    noteBuildReject('"' + name + '" 已经在牌组里了，普通地同样受单卡限制。');
    return;
  }
  if (buildCards.length + (buildCommanders.length || (buildCommander ? 1 : 0)) >= BUILD_TARGET) return;
  buildChosen.push(normalizeBuildName(name));
  buildCards.push({ name, card: { name, type_line: 'Land' } });
  renderBuilderSidebar();
  if (isBuilderComplete()) {
    if (!hasShownCompletePulse) {
      hasShownCompletePulse = true;
      builderSidebar.classList.add('complete-pulse');
      setTimeout(() => builderSidebar.classList.remove('complete-pulse'), 2000);
    }
  } else {
    refreshIfCandidateCollides();
  }
}

function isBuilderComplete() {
  return buildCards.length + (buildCommanders.length || (buildCommander ? 1 : 0)) >= BUILD_TARGET;
}

// Track whether we've shown the 100-card completion animation. Reset when dropping
// below 100 or starting a new build, so users get visual feedback each time they
// complete the deck after editing.
let hasShownCompletePulse = false;

// After a quick-add (land / basic / staple / candidate) puts a card into the draft,
// refresh the 3-choose-1 hand if any currently shown candidate is now already chosen.
// This keeps the visible hand from lingering on a card the user just added, without
// reordering or de-duplicating the random pool itself (scheme A).
function refreshIfCandidateCollides() {
  if (!buildCandidates.length || !buildCommander) return;
  const collided = buildCandidates.some((candidate) => {
    const key = normalizeBuildName(candidate.name);
    return buildChosen.includes(key) || buildCards.some((card) => normalizeBuildName(card.name) === key);
  });
  if (collided) nextBuildBatch();
}

async function startBuild() {
  const name = builderCommanderInput.value.trim();
  if (!name) {
    builderMessage.textContent = '请输入主将名称。';
    return;
  }
  builderMessage.textContent = '';
  builderStartButton.disabled = true;
  builderStartButton.textContent = '加载中…';
  try {
    // Resolve the (single, or already-paired) commander(s) for color identity and
    // display data; the build-suggest call below only seeds the candidate hand.
    // If the user hand-typed a name that differs from the last resolved/random
    // commander, treat the input as a fresh single commandere (dropping any stale
    // partner state) rather than re-resolving the previous selection.
    const inputDiffers = !buildCommanders.length || buildCommanders.map((c) => c.name).join(' // ') !== name;
    const commanders = inputDiffers ? [{ name, card: null, is_partner: false }] : buildCommanders;
    const resolved = await resolveCommanderPreview(commanders.map((c) => c.name));
    if (resolved.error) {
      builderMessage.textContent = resolved.error;
      return;
    }
    buildCommanders = (resolved.commanders || []).map((c) => ({ name: c.name, card: c.card, is_partner: c.is_partner }));
    buildColors = (resolved.color_identity || []).slice();
    buildCommander = buildCommanders.map((c) => c.name).join(' // ');
    renderCommanderPreview(buildCommanders);
    syncPartnerUI(buildCommanders);

    const response = await fetch('/api/v1/build-suggest', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ commander: buildCommanders[0].name, chosen: [], seen: [], count: 3 })
    });
    const payload = await response.json();
    if (!response.ok) throw new Error(payload.error?.message || '无法加载主将建议。');
    buildChosen = [];
    buildCards = [];
    recentShown = [];
    hasShownCompletePulse = false;
    builderWorkflow.hidden = false;
    builderComplete.hidden = true;
    const first = Array.isArray(payload.candidates) ? payload.candidates : [];
    rememberShown(first);
    applyBuildCandidates(first);
    renderBuilderSidebar();
  } catch (error) {
    builderMessage.textContent = error.message || '加载失败，请重试。';
  } finally {
    builderStartButton.disabled = false;
    builderStartButton.textContent = '开始';
  }
}

function applyBuildCandidates(candidates) {
  buildCandidates = Array.isArray(candidates) ? candidates : [];
  builderCandidates.innerHTML = buildCandidates.map((card, index) => {
    const image = cardImage(card.card);
    const preview = cardPreviewImage(card.card);
    const cardText = previewTextFor(card.card);
    const fills = (card.fills || []).map((id) => buildMetricLabel(id)).filter(Boolean).join(' · ');
    const gcBadge = card.game_changer ? '<span class="builder-gc-tag" title="Game Changer">GC</span>' : '';
    return `
      <button type="button" class="builder-candidate" data-candidate="${index}" data-preview-src="${escapeHTML(preview)}" data-preview-name="${escapeHTML(card.name)}" data-card-text="${escapeHTML(cardText)}">
        ${image ? `<img loading="lazy" src="${escapeHTML(image)}" alt="${escapeHTML(card.name)}">` : '<div class="builder-candidate-placeholder"></div>'}
        <div class="builder-candidate-body">
          <div class="builder-candidate-title">${gcBadge}<strong>${escapeHTML(card.name)}</strong></div>
          <span class="builder-synergy">Synergy ${(Number(card.synergy) || 0).toFixed(2)}%</span>
          ${fills ? `<small>补足：${escapeHTML(fills)}</small>` : ''}
        </div>
      </button>`;
  }).join('') || '<p class="editor-empty">暂时没有更多建议，可快速加基本地或直接完成。</p>';
}

function cardImage(card) {
  const cardObj = card || {};
  return cardObj.image_small || cardObj.image_normal || (cardObj.faces || []).find((face) => face.image_small || face.image_normal)?.image_small || (cardObj.faces || []).find((face) => face.image_small || face.image_normal)?.image_normal || '';
}

// Prefer the larger face image for hover previews; falls back to the small grid
// image when the card has no normal-size art.
function cardPreviewImage(card) {
  const cardObj = card || {};
  return cardObj.image_normal || cardObj.image_small || (cardObj.faces || []).find((face) => face.image_normal || face.image_small)?.image_normal || (cardObj.faces || []).find((face) => face.image_normal || face.image_small)?.image_small || '';
}

function buildMetricLabel(id) {
  const metric = BUILD_METRICS.find((item) => item.id === id);
  return metric ? metric.label : id;
}

async function nextBuildBatch() {
  if (!buildCommander) return;
  // Every refresh draws a fresh random hand straight from the server. Cards shown
  // (but not chosen) in the last two refreshes are sent as `seen` so the server can
  // avoid bouncing them straight back; chosen cards remain permanently excluded.
  builderCandidates.innerHTML = '<p class="editor-empty">正在换一批…</p>';
  try {
    const response = await fetch('/api/v1/build-suggest', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ commander: buildCommander, chosen: buildChosen, seen: recentShown, count: 3 })
    });
    const payload = await response.json();
    if (!response.ok) throw new Error(payload.error?.message || '无法加载建议。');
    const candidates = Array.isArray(payload.candidates) ? payload.candidates : [];
    rememberShown(candidates);
    applyBuildCandidates(candidates);
  } catch (error) {
    builderCandidates.innerHTML = `<p class="form-message">${escapeHTML(error.message || '加载失败')}</p>`;
  }
}

// Record the cards just shown (only the ones not chosen yet) into a two-refresh
// sliding window, so the next two draws avoid re-offering them. We track by the
// normalized name; duplicates are collapsed.
function rememberShown(candidates) {
  const current = candidates.map((c) => normalizeBuildName(c.name)).filter(Boolean);
  recentShown = recentShown.concat(current);
  // Keep only the most recent two refreshes (up to 6 unique-ish entries).
  const windowSize = 6;
  const seen = new Set();
  const next = [];
  for (let i = recentShown.length - 1; i >= 0 && next.length < windowSize; i--) {
    const key = recentShown[i];
    if (key && !seen.has(key)) {
      seen.add(key);
      next.unshift(key);
    }
  }
  recentShown = next;
}

// Normalize a card name to its front-face, lowercased form so split cards
// ("X // Y") dedupe against the same card offered by its front face ("X").
function normalizeBuildName(name) {
  const trimmed = String(name ?? '').trim().toLowerCase();
  const idx = trimmed.indexOf(' // ');
  return idx > 0 ? trimmed.slice(0, idx).trim() : trimmed;
}

function addBuildCard(candidate) {
  if (!candidate?.name) return;
  const key = normalizeBuildName(candidate.name);
  if (buildChosen.includes(key)) return;
  if (!canAddBuildCard(candidate.name)) {
    noteBuildReject('"' + candidate.name + '" 是重复普通牌，只能放一张（基本地除外）。');
    return;
  }
  buildChosen.push(key);
  const entry = { name: candidate.name, card: candidate.card || {} };
  if (candidate.game_changer) entry.game_changer = true;
  buildCards.push(entry);
  renderBuilderSidebar();
  if (isBuilderComplete()) {
    if (!hasShownCompletePulse) {
      hasShownCompletePulse = true;
      builderSidebar.classList.add('complete-pulse');
      setTimeout(() => builderSidebar.classList.remove('complete-pulse'), 2000);
    }
  } else {
    // Fetch a fresh random hand immediately; the un-picked cards vanish with it.
    nextBuildBatch();
  }
}

function addBasicLand(type) {
  const key = type.toLowerCase();
  const count = buildCards.filter((card) => card.name.toLowerCase() === key).length;
  // Basic lands (including the colorless Wastes) may appear in multiples; still avoid
  // exceeding the 100-card ceiling.
  if (buildCards.length + (buildCommanders.length || (buildCommander ? 1 : 0)) >= BUILD_TARGET) return;
  buildCards.push({ name: type, card: { name: type, type_line: `Basic Land — ${type}` } });
  renderBuilderSidebar();
  if (isBuilderComplete()) {
    if (!hasShownCompletePulse) {
      hasShownCompletePulse = true;
      builderSidebar.classList.add('complete-pulse');
      setTimeout(() => builderSidebar.classList.remove('complete-pulse'), 2000);
    }
  } else {
    refreshIfCandidateCollides();
  }
}

// Remove one copy of the named card from the draft. When the last copy is removed the
// name also leaves buildChosen, so the 3-choose-1 pool may offer it again on a later
// refresh. Reopening the completed deck (if the builder had finished) is left to the
// caller, but here we simply re-show the workflow if the draft drops below 100.
// Remove one copy of the named card from the draft. When the last copy is removed the
// name also leaves buildChosen, so the 3-choose-1 pool may offer it again on a later
// refresh. When dropping below 100, reset the pulse flag so the next completion shows
// the green border animation again.
function removeBuildCard(name) {
  if (!name) return;
  const index = buildCards.findIndex((card) => normalizeBuildName(card.name) === normalizeBuildName(name));
  if (index < 0) return;
  buildCards.splice(index, 1);
  const key = normalizeBuildName(name);
  const stillPresent = buildCards.some((card) => normalizeBuildName(card.name) === key);
  if (!stillPresent) {
    buildChosen = buildChosen.filter((chosen) => chosen !== key);
  }
  hasShownCompletePulse = false;
  renderBuilderSidebar();
}

// "继续微调" from the completed state: keep the workflow open so the user can keep
// editing the 100-card draft without ever exceeding the target.
function backToBuilderEdit() {
  builderComplete.hidden = true;
  builderWorkflow.hidden = false;
  renderBuilderSidebar();
}

function renderBuilderSidebar() {
  const current = {};
  for (const item of buildCards) {
    const card = item.card || {};
    for (const metric of BUILD_METRICS) {
      if (builderCardMatches(metric.id, card)) {
        current[metric.id] = (current[metric.id] || 0) + 1;
      }
    }
  }
  const total = buildCards.length + (buildCommanders.length || (buildCommander ? 1 : 0));

  // Aggregate the drafted mainboard by name so the list shows "2× Sol Ring" style
  // rows. Card objects are kept so a later hover preview can reuse their art.
  const byName = new Map();
  for (const item of buildCards) {
    const key = normalizeBuildName(item.name);
    const entry = byName.get(key) || { name: item.name, count: 0, card: item.card, game_changer: false };
    entry.count += 1;
    if (!entry.card?.name) entry.card = item.card;
    if (item.game_changer) entry.game_changer = true;
    byName.set(key, entry);
  }
  const chosenList = Array.from(byName.values())
    .sort((a, b) => String(a.name).localeCompare(String(b.name)))
    .map((entry) => `
      <li class="builder-chosen-item" data-preview-src="${escapeHTML(cardPreviewImage(entry.card) || '')}" data-preview-name="${escapeHTML(entry.name)}" data-card-text="${escapeHTML(previewTextFor(entry.card))}">
        <span class="builder-chosen-name">${entry.game_changer ? '<span class="builder-gc-tag" title="Game Changer">GC</span>' : ''}${escapeHTML(entry.name)}</span>
        <span class="builder-chosen-side">
          <strong class="builder-chosen-count">${entry.count}×</strong>
          <button type="button" class="builder-chosen-remove" data-remove-name="${escapeHTML(entry.name)}" title="移除一张" aria-label="移除 ${escapeHTML(entry.name)}">−</button>
        </span>
      </li>`).join('');

  const commanderBox = buildCommanders.length ? `
    <div class="builder-commander-box">
      <div class="builder-commander-box-head"><strong>主将</strong><span>${buildCommanders.length}</span></div>
      <ul class="builder-commander-box-list">${buildCommanders.map((commander) => {
        const card = commander.card || {};
        const image = cardImage(card) || cardPreviewImage(card);
        return `<li class="builder-commander-box-item" data-preview-src="${escapeHTML(cardPreviewImage(card) || image)}" data-preview-name="${escapeHTML(commander.name)}" data-card-text="${escapeHTML(previewTextFor(card))}">${image ? `<img loading="lazy" src="${escapeHTML(image)}" alt="${escapeHTML(commander.name)}">` : '<div class="builder-candidate-placeholder"></div>'}<span>${escapeHTML(commander.name)}</span></li>`;
      }).join('')}</ul>
    </div>` : '';

  builderSidebar.innerHTML = `
    <div class="builder-progress"><strong>${total}</strong><span>/ ${BUILD_TARGET} 张</span></div>
    ${commanderBox}
    ${BUILD_METRICS.map((metric) => {
      const actual = current[metric.id] || 0;
      const pct = Math.min(100, Math.round((actual / Math.max(1, metric.target)) * 100));
      return `<div class="builder-metric"><div class="builder-metric-head"><span>${metric.label}</span><strong>${actual} / ${metric.target}</strong></div><div class="builder-metric-bar"><i style="width:${pct}%"></i></div></div>`;
    }).join('')}
    <div class="builder-color-identity"><span>主将色组</span><strong>${(buildColors || []).map((color) => escapeHTML(color)).join(' ') || '无色'}</strong></div>
    <div class="builder-chosen">
      <div class="builder-chosen-head"><strong>已选牌</strong><span>${buildCards.length}</span></div>
      ${chosenList ? `<ul class="builder-chosen-list">${chosenList}</ul>` : '<p class="builder-chosen-empty">还没有选择任何牌。</p>'}
    </div>`;
}

// A client-side mirror of the server's construction.Classify for live "正向法力"
// and category counts. It intentionally treats "lands" as "net-positive mana": a
// land OR a 0-cost artifact that produces mana (Sol Ring, Mox, Lotus Petal).
function builderCardMatches(id, card) {
  const text = String((card.oracle_text || '') + ' ' + (card.faces || []).map((face) => face.oracle_text || '').join(' ')).toLowerCase();
  const typeLine = String((card.type_line || '') + ' ' + (card.faces || []).map((face) => face.type_line || '').join(' ')).toLowerCase();
  const cmc = Number(card.cmc);
  switch (id) {
    case 'lands': {
      const isLand = typeLine.includes('land');
      const isFastMana = cmc === 0 && typeLine.includes('artifact') && /\badd\b/.test(text);
      return isLand || isFastMana;
    }
    case 'mass_interaction':
      return text.includes('each player') || text.includes('all creatures') || text.includes('destroy all') || text.includes('exile all');
    case 'single_interaction':
      return text.includes('target') && (text.includes('destroy') || text.includes('exile') || text.includes('counter target') || text.includes('return target'));
    case 'draw_discard':
      return text.includes('draw a card') || text.includes('draw cards') || text.includes('draw that many') || text.includes('discard');
    case 'ramp':
      return !typeLine.includes('land') && (text.includes('add {') || text.includes('additional land') || text.includes('search your library for a basic land') || text.includes('costs {'));
    case 'plan':
      return text.includes('token') || text.includes('proliferate') || text.includes('infect') || text.includes('poison') || text.includes('whenever');
    default:
      return false;
  }
}

function builderToDeckText() {
  const commanderNames = buildCommanders.length ? buildCommanders.map((c) => c.name) : (buildCommander ? [buildCommander] : []);
  const commanderLines = commanderNames.map((name) => `1 ${name}`);
  const counts = new Map();
  for (const card of buildCards) {
    const key = normalizeBuildName(card.name);
    // Keep the canonical (Scryfall-origin) name for display, but count by key so
    // split/DFC reprints of the same card still aggregate correctly.
    if (!counts.has(key)) counts.set(key, { name: card.name, count: 0 });
    counts.get(key).count += 1;
  }
  const mainboardLines = Array.from(counts.values()).map((entry) => `${entry.count} ${entry.name}`);
  return `Commander\n${commanderLines.join('\n')}\n\nDeck\n${mainboardLines.join('\n')}`;
}

buildEntryButton.addEventListener('click', openBuilder);
builderClose.addEventListener('click', closeBuilder);
builderStartButton.addEventListener('click', startBuild);
builderSkip.addEventListener('click', nextBuildBatch);
builderLandsButton.addEventListener('click', toggleLandsPanel);
builderStaplesButton.addEventListener('click', toggleStaplesPanel);
builderRandomButton.addEventListener('click', pickRandomCommander);
builderPartnerAddButton.addEventListener('click', addPartnerCommander);
// Collapse/expand the two provider result cards when their top bar is clicked.
document.addEventListener('click', (event) => {
  const toggle = event.target.closest('[data-card-toggle]');
  if (!toggle) return;
  const card = toggle.closest('.result-card');
  if (!card) return;
  const collapsed = card.classList.toggle('collapsed');
  toggle.setAttribute('aria-expanded', String(!collapsed));
});
// Expand/collapse the CommanderSalt suggestion groups whose contents default to
// collapsed (当前档位原因 / 提高强度建议). Delegated so dynamically-inserted groups work.
document.addEventListener('click', (event) => {
  const toggle = event.target.closest('[data-suggestion-toggle]');
  if (!toggle) return;
  const group = toggle.closest('[data-suggestion-group]');
  if (!group) return;
  const collapsed = group.classList.toggle('is-collapsed');
  toggle.setAttribute('aria-expanded', String(!collapsed));
});

// Collapse/expand the big result sections (法术力基础 / 构筑概览 / 关联卡牌 / 构筑缺口推荐 /
// 完整牌表). Delegated so clicking the heading or its "收起/展开" chip toggles the whole
// section; the deck-actions inside a heading are NOT toggled by way of being sibling
// content, so the copy/export buttons stay usable regardless.
document.addEventListener('click', (event) => {
  const toggle = event.target.closest('[data-section-toggle]');
  if (!toggle) return;
  // Ignore clicks on action buttons nested inside the header (copy decklist, etc.),
  // but allow the section-heading-toggle button itself
  if (event.target.closest('[data-section-actions]') && !event.target.closest('.section-heading-toggle')) return;
  const section = toggle.closest('.catalog-section');
  if (!section) return;
  const collapsed = section.classList.toggle('is-collapsed');
  toggle.setAttribute('aria-expanded', String(!collapsed));
  const chip = toggle.querySelector('.section-heading-toggle');
  if (chip) chip.textContent = collapsed ? '展开' : '收起';
});
builderExport.addEventListener('click', () => downloadText('decklist.txt', builderToDeckText()));
builderAnalyze.addEventListener('click', () => {
  decklistInput.value = builderToDeckText();
  closeBuilder();
  analyze();
});
builderBackEdit.addEventListener('click', backToBuilderEdit);

// Delegate builder candidate clicks through the global click listener. Basic lands
// are wired here, self-contained and gated to the commander's color identity is
// optional; all five are offered for simplicity.
document.addEventListener('click', (event) => {
  const candidateButton = event.target.closest('[data-candidate]');
  if (candidateButton) {
    const index = Number(candidateButton.dataset.candidate);
    const candidate = buildCandidates[index];
    if (candidate) addBuildCard(candidate);
    return;
  }
  const landCategory = event.target.closest('[data-land-category]');
  if (landCategory) {
    loadLandCategory(landCategory.dataset.landCategory || '');
    return;
  }
  const landCard = event.target.closest('[data-land-name]');
  if (landCard) {
    addLandCard(landCard.dataset.landName || '');
    return;
  }
  const stapleCategory = event.target.closest('[data-staple-category]');
  if (stapleCategory) {
    loadStapleCategory(stapleCategory.dataset.stapleCategory || '');
    return;
  }
  const stapleCard = event.target.closest('[data-staple-name]');
  if (stapleCard) {
    let card = {};
    try {
      card = JSON.parse(stapleCard.dataset.stapleCard || '{}');
    } catch {
      card = {};
    }
    const isGc = stapleCard.dataset.stapleGc === '1';
    addStapleCard(stapleCard.dataset.stapleName || '', card, isGc);
    return;
  }
  const basicButton = event.target.closest('[data-basic]');
  if (basicButton) {
    addBasicLand(basicButton.dataset.basic || '');
    return;
  }
  const removeButton = event.target.closest('[data-remove-name]');
  if (removeButton) {
    removeBuildCard(removeButton.dataset.removeName || '');
    return;
  }
});

copyDecklistButton.addEventListener('click', async () => {
  if (!currentDeckText) return;
  try {
    await navigator.clipboard.writeText(currentDeckText);
    copyDecklistButton.textContent = '已复制';
    setTimeout(() => { copyDecklistButton.textContent = '复制牌表'; }, 1600);
  } catch {
    copyDecklistButton.textContent = '复制失败';
    setTimeout(() => { copyDecklistButton.textContent = '复制牌表'; }, 1600);
  }
});

// Delegate decklist expand/collapse to the container
document.addEventListener('click', (event) => {
  const expandTrigger = event.target.closest('[data-decklist-expand]');
  if (expandTrigger) {
    const list = document.querySelector('#deck-card-list');
    if (list) list.classList.remove('collapsed');
    return;
  }
  const collapseTrigger = event.target.closest('[data-decklist-collapse]');
  if (collapseTrigger) {
    const list = document.querySelector('#deck-card-list');
    if (list) {
      list.classList.add('collapsed');
      list.scrollIntoView({ behavior: 'smooth', block: 'start' });
    }
    return;
  }
});

// Theme toggle
themeToggle.addEventListener('click', () => {
  const root = document.documentElement;
  const isLight = root.classList.toggle('light-mode');
  themeToggle.textContent = isLight ? '亮色' : '暗色';
  localStorage.setItem('theme', isLight ? 'light' : 'dark');
});

// Load saved theme preference
if (localStorage.getItem('theme') === 'light') {
  document.documentElement.classList.add('light-mode');
  themeToggle.textContent = '亮色';
}

form.addEventListener('submit', async (event) => {
  event.preventDefault();
  await analyze();
});

retryButton.addEventListener('click', () => {
  results.hidden = true;
  input.focus();
  window.scrollTo({ top: 0, behavior: 'smooth' });
});

clearDecklistButton.addEventListener('click', () => {
  decklistInput.value = '';
  decklistInput.focus();
});

async function analyze() {
  const url = input.value.trim();
  const decklist = decklistInput.value.trim();
  message.textContent = '';
  if (!url && !decklist) {
    message.textContent = '请填写 Moxfield URL 或粘贴牌表文本。';
    input.focus();
    return;
  }
  if (url && !isMoxfieldDeckURL(url)) {
    message.textContent = '请输入有效的公开 Moxfield 牌组地址。';
    input.focus();
    return;
  }

  setLoading(true);
  results.hidden = true;
  try {
    const response = await fetch('/api/v1/analyze', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ url, decklist })
    });
    const payload = await response.json();
    if (!response.ok) {
      throw new Error(payload.error?.message || '分析失败，请稍后重试。');
    }
    render(payload);
  } catch (error) {
    message.textContent = error.message || '网络请求失败，请稍后重试。';
  } finally {
    setLoading(false);
  }
}

function isMoxfieldDeckURL(value) {
  try {
    const parsed = new URL(value);
    return parsed.protocol === 'https:' &&
      ['moxfield.com', 'www.moxfield.com'].includes(parsed.hostname.toLowerCase()) &&
      /^\/decks\/[A-Za-z0-9_-]{6,64}\/?$/.test(parsed.pathname);
  } catch {
    return false;
  }
}

function setLoading(active) {
  loading.hidden = !active;
  const skeleton = document.querySelector('#skeleton');
  if (skeleton) skeleton.hidden = !active;
  submitButton.disabled = active;
  submitButton.querySelector('.button-label').textContent = active ? '分析中…' : '开始分析';
}

function render(payload) {
  const skeleton = document.querySelector('#skeleton');
  if (skeleton) skeleton.hidden = true;
  document.querySelector('#deck-name').textContent = payload.deck.name || payload.deck.commanders.join(' / ');
  document.querySelector('#deck-meta').textContent = `${payload.deck.commanders.join(' / ')} · ${payload.deck.card_count} 张牌`;
  renderProvider('salt', payload.results.commandersalt, [
    ['salt', 'Salt']
  ]);
  renderProvider('edh', payload.results.edhpowerlevel, [
    ['efficiency', 'Efficiency'], ['impact', 'Impact'],
    ['score', 'Score'], ['average_playability', 'Playability']
  ]);
  warning.hidden = !payload.warnings?.length;
  warning.textContent = payload.warnings?.join(' ') || '';
  renderManabase(payload.manabase);
  renderConstructionReport(payload.construction_report);
  renderCombos(payload.combos || []);
  renderRecommendations(payload.recommendations || [], payload.recommendation_keywords || []);
  renderDeckCards(payload.deck_cards || []);
  currentDeckCards = payload.deck_cards || [];
  currentDeckText = payload.canonical_decklist || buildDeckText(currentDeckCards);
  beginEditing(payload.deck?.id || '', currentDeckCards);
  results.hidden = false;
  results.scrollIntoView({ behavior: 'smooth', block: 'start' });
  
  // Show sticky navigation
  showStickyNav();
  
  // Initialize scroll-triggered animations after content is rendered
  setTimeout(() => initScrollAnimations(), 100);
}

function renderManabase(manabase) {
  const section = document.querySelector('#manabase-section');
  const container = document.querySelector('#manabase-content');
  if (!manabase) {
    section.hidden = true;
    return;
  }
  section.hidden = false;

  const target = Number(manabase.target_lands);
  const actual = Number(manabase.actual_lands);
  const delta = Number(manabase.land_delta);
  const deltaText = delta > 0 ? `+${formatNumber(delta, 1)}` : formatNumber(delta, 1);
  const deltaClass = delta >= -1 ? 'healthy' : delta >= -2 ? 'warn' : 'short';
  const avgMv = formatNumber(manabase.average_mana_value, 2);
  const ramp = Number(manabase.ramp_and_draw_under_three || 0);
  const fastMana = Number(manabase.fast_mana || 0);
  const findings = Array.isArray(manabase.color_findings) ? manabase.color_findings : [];
  const costCounts = Array.isArray(manabase.cost_counts) ? manabase.cost_counts : [];

  const landDeltaLabel = {
    healthy: '地数充足',
    warn: '接近目标',
    short: '地数不足'
  }[deltaClass];

  // 法术力构成：按颜色法术力标的（蓝/白/黑/红/绿）统计牌组需要多少个彩色法术力符号。
  // 每根的填充长度 = 该色符文数 / 全部彩色符文总数：不足 100% 的部分是其它颜色
  // 占掉的比例，让各颜色需求比例一眼可读。
  const colorPips = (manabase.color_pips || {});
  let composition = '';
  if (Object.keys(colorPips).length) {
    const totalPips = Object.values(colorPips).reduce((sum, n) => sum + (Number(n) || 0), 0);
    const pipRows = MANA_COLORS.map((color) => {
      const count = Number(colorPips[color] || 0);
      const pct = totalPips > 0 ? Math.round((count / totalPips) * 100) : 0;
      const symbol = manaSymbolFor(color);
      return `
        <div class="manabase-pip-row" data-color="${color}">
          <span class="mana-symbol ${color.toLowerCase()}">${symbol}</span>
          <div class="manabase-pip-track"><i data-pct="${pct}"></i></div>
          <strong>${count}</strong>
        </div>`;
    }).join('');
    composition = `
      <div class="manabase-table-heading"><span>法术力构成</span><small>各颜色法术力符号数量</small></div>
      <div class="manabase-pips">${pipRows}</div>`;
  }

  const curveHTML = costCounts.length ? (() => {
    const max = Math.max(1, ...costCounts.map((bucket) => Number(bucket.count) || 0));
    // Scale against the maximum, but keep a visible floor so a 0-count bucket still
    // shows a short stub rather than disappearing. The final height is written as a
    // data attribute and animated in via a transition after the container is rendered.
    const rows = costCounts.map((bucket) => {
      const count = Number(bucket.count) || 0;
      const minH = 8;
      const pct = max > 0 ? count / max : 0;
      const barH = minH + pct * (100 - minH);
      return `<div class="manabase-curve-row" data-mana-value="${bucket.mana_value ?? ''}">
        <span class="manabase-curve-label">${escapeHTML(bucket.label ?? '')}</span>
        <div class="manabase-curve-bar"><i data-height="${barH.toFixed(1)}"></i></div>
        <strong class="manabase-curve-count">${count}</strong>
      </div>`;
    }).join('');
    return `
      <div class="manabase-table-heading"><span>法术力曲线</span><small>非地牌 · 含主将与加速物</small></div>
      <div class="manabase-curve">${rows}</div>`;
  })() : '';

  container.innerHTML = `
    <div class="manabase-hero">
      <div class="manabase-land-stat"><span>实际地数</span><strong>${Number.isFinite(actual) ? actual : '—'}</strong></div>
      <div class="manabase-land-stat"><span>推荐地数</span><strong>${Number.isFinite(target) ? formatNumber(target, 1) : '—'}</strong></div>
      <div class="manabase-land-stat ${deltaClass}"><span>偏差</span><strong>${deltaText}</strong><small>${landDeltaLabel}</small></div>
    </div>
    <p class="manabase-formula">平均法术力值 <strong>${avgMv}</strong> · 地牌/抽牌信用 <strong>${ramp}</strong> · 快法力 <strong>${fastMana}</strong></p>
    ${composition}
    ${curveHTML}
    ${findings.length ? `
      <div class="manabase-colors-heading"><span>颜色来源需求</span><small>需要 vs 当前（加权）</small></div>
      <div class="manabase-color-grid">${findings.map(renderColorFinding).join('')}</div>` : ''}`;

  // Bars start at height 0 (via CSS) and grow to their data-height on the next frame,
  // so the curve animates in rather than jumping to final size.
  // 颜色比例条也在这里用像素宽度着色，而不是依赖内联百分比宽度：某些渲染器（含
  // IAB）会把对内联百分比的解析忽略掉，导致每根都被拉满。像素宽度对该选择器内
  // 的元素不会歧义，始终正确。
  requestAnimationFrame(() => {
    container.querySelectorAll('.manabase-curve-bar i').forEach((bar) => {
      const height = parseFloat(bar.dataset.height) || 0;
      bar.style.height = height + 'px';
    });
    container.querySelectorAll('.manabase-pip-track i').forEach((bar) => {
      const pct = parseFloat(bar.dataset.pct) || 0;
      const track = bar.parentElement;
      const trackW = track ? track.getBoundingClientRect().width : 0;
      bar.style.width = trackW > 0 ? Math.round((pct / 100) * trackW) + 'px' : '0px';
    });
  });
}

function renderColorFinding(finding) {
  const color = escapeHTML(String(finding.color ?? ''));
  const actual = Number(finding.actual_sources);
  const required = Number(finding.required_sources);
  const deficit = required - actual;
  const adequate = !Number.isFinite(deficit) || deficit <= 0;
  const pct = required > 0 ? Math.min(100, Math.round((actual / required) * 100)) : 100;
  const driving = String(finding.driving_spell ?? '').trim();
  const symbol = manaSymbolFor(String(finding.color ?? '').toUpperCase());
  return `
    <div class="manabase-color ${adequate ? 'adequate' : 'short'}">
      <div class="manabase-color-head"><span class="mana-symbol ${color.toLowerCase()}">${symbol}</span><div><strong>${required}</strong> 需求来源 / <strong>${formatNumber(actual, 1)}</strong> 当前</div></div>
      <div class="manabase-color-bar"><i style="width:${pct}%"></i></div>
      <div class="manabase-color-meta">${adequate ? '来源充足' : `缺少约 ${formatNumber(deficit, 1)} 个来源`}${driving ? ` · 需求由 ${escapeHTML(driving)} 的多色费用驱动` : ''}</div>
    </div>`;
}

function renderConstructionReport(report) {
  const section = document.querySelector('#construction-section');
  const container = document.querySelector('#construction-metrics');
  const metrics = Array.isArray(report?.metrics) ? report.metrics : [];
  section.hidden = !metrics.length;
  container.innerHTML = metrics.map((metric) => {
    const percent = Math.min(100, Math.round((Number(metric.actual) / Math.max(1, Number(metric.target))) * 100));
    const cards = (metric.cards || []).map((card) => `<li><strong>${card.quantity}× ${escapeHTML(card.name)}</strong><span>${escapeHTML(card.reason)}</span></li>`).join('');
    return `<details class="construction-metric ${metric.status}">
      <summary><div><span>${escapeHTML(metric.label)}</span><strong>${metric.actual} / ${metric.target}</strong></div><div class="construction-bar"><i data-pct="${percent}"></i></div><small>${metric.gap > 0 ? `缺少 ${metric.gap}` : '已充分'}</small></summary>
      ${cards ? `<ul>${cards}</ul>` : '<p>没有识别到相关卡牌。</p>'}
    </details>`;
  }).join('');

  // Fill bars by pixel width on the next frame instead of inline percent width, for
  // the same reason the manabase pip bars do: some renderers (the IAB included) drop
  // inline percentage widths and stretch every bar full.
  requestAnimationFrame(() => {
    container.querySelectorAll('.construction-bar i').forEach((bar) => {
      const pct = parseFloat(bar.dataset.pct) || 0;
      const track = bar.parentElement;
      const trackW = track ? track.getBoundingClientRect().width : 0;
      bar.style.width = trackW > 0 ? Math.round((pct / 100) * trackW) + 'px' : '0px';
    });
  });
}

function buildDeckText(cards) {
  const commanders = cards.filter((item) => item.commander).map((item) => `${item.quantity || 1} ${item.card?.name || ''}`);
  const mainboard = cards.filter((item) => !item.commander).map((item) => `${item.quantity || 1} ${item.card?.name || ''}`);
  return `Commander\n${commanders.join('\n')}\n\nDeck\n${mainboard.join('\n')}`;
}

function openSwap(addName) {
  pendingSwapAdd = addName;
  selectedSwapRemove = '';
  document.querySelector('#swap-add-name').textContent = addName;
  swapSearch.value = '';
  swapMessage.textContent = '';
  swapResult.hidden = true;
  swapSubmit.disabled = true;
  renderSwapRemoveList('');
  swapModal.hidden = false;
  swapSearch.focus();
}

function closeSwap() {
  swapModal.hidden = true;
  pendingSwapAdd = '';
  selectedSwapRemove = '';
}

function renderSwapRemoveList(query) {
  const normalized = query.trim().toLowerCase();
  const cards = currentDeckCards.filter((item) => !item.commander && (!normalized || String(item.card?.name || '').toLowerCase().includes(normalized)));
  const groups = [
    ['Nonlands', cards.filter((item) => !item.land)],
    ['Lands', cards.filter((item) => item.land)]
  ];
  document.querySelector('#swap-remove-list').innerHTML = groups.filter(([, items]) => items.length).map(([label, items]) => `<section><h3>${label}</h3>${items.map((item) => `<button type="button" class="swap-remove-option${selectedSwapRemove === item.card.name ? ' selected' : ''}" data-remove-name="${escapeHTML(item.card.name)}"><span>${item.quantity}×</span><strong>${escapeHTML(item.card.name)}</strong></button>`).join('')}</section>`).join('') || '<p>没有匹配的 Mainboard 卡牌。</p>';
}

async function compareSwap() {
  if (!currentDeckText || !pendingSwapAdd || !selectedSwapRemove) return;
  swapSubmit.disabled = true;
  swapSubmit.textContent = '比较中…';
  swapMessage.textContent = '';
  try {
    const response = await fetch('/api/v1/compare-swap', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ decklist: currentDeckText, remove_name: selectedSwapRemove, add_name: pendingSwapAdd })
    });
    const payload = await response.json();
    if (!response.ok) throw new Error(payload.error?.message || '替换比较失败。');
    renderSwapResult(payload);
  } catch (error) {
    swapMessage.textContent = error.message || '替换比较失败。';
  } finally {
    swapSubmit.disabled = !selectedSwapRemove;
    swapSubmit.textContent = '比较替换';
  }
}

function renderSwapResult(payload) {
  const deltas = (payload.deltas || []).map((metric) => {
    const delta = Number(metric.delta || 0);
    const deltaText = delta > 0 ? `+${delta}` : String(delta);
    return `<tr><th>${escapeHTML(metric.label)}</th><td>${metric.before}</td><td>${metric.after}</td><td class="swap-delta ${delta > 0 ? 'positive' : delta < 0 ? 'negative' : 'neutral'}">${deltaText}</td></tr>`;
  }).join('');
  const issues = (payload.legality?.issues || []).map((issue) => `<li>${escapeHTML(issue)}</li>`).join('');
  swapResult.innerHTML = `<div class="swap-result-title"><strong>${escapeHTML(payload.removed?.name || '')}</strong><span>→</span><strong>${escapeHTML(payload.added?.name || '')}</strong></div>
    <table><thead><tr><th>指标</th><th>替换前</th><th>替换后</th><th>变化</th></tr></thead><tbody>${deltas}</tbody></table>
    <div class="swap-legality ${payload.legality?.valid ? 'valid' : 'warning'}"><strong>基础合法性：${payload.legality?.valid ? '通过' : '需要注意'}</strong><span>牌张数 ${payload.before?.card_count} → ${payload.after?.card_count} · Commander 色组 ${(payload.legality?.color_identity || []).join('') || '无色'}</span>${issues ? `<ul>${issues}</ul>` : ''}</div>
    <textarea readonly aria-label="更新后的牌表">${escapeHTML(payload.updated_decklist || '')}</textarea>
    <button id="copy-swap-decklist" class="ghost-button" type="button">复制更新后牌表</button>`;
  swapResult.hidden = false;
  document.querySelector('#copy-swap-decklist').addEventListener('click', async (event) => {
    try {
      await navigator.clipboard.writeText(payload.updated_decklist || '');
      event.currentTarget.textContent = '已复制';
    } catch {
      event.currentTarget.textContent = '复制失败';
    }
  });
}

function renderCombos(combos) {
  const section = document.querySelector('#combo-section');
  const container = document.querySelector('#combo-list');
  section.hidden = !combos.length;
  container.innerHTML = combos.map((combo) => `
    <article class="combo-card">
      <div class="combo-header"><div><span class="source-badge">COMMANDER SPELLBOOK</span><h3>${escapeHTML(combo.name)}</h3></div>${combo.source_url ? `<a href="${escapeHTML(combo.source_url)}" target="_blank" rel="noopener noreferrer">查看来源 ↗</a>` : ''}</div>
      <div class="combo-components">${(combo.components || []).map((item) => renderCard({ ...item, editor: false })).join('')}</div>
      ${combo.result ? `<p class="combo-result"><strong>结果</strong>${escapeHTML(combo.result)}</p>` : ''}
      ${combo.steps?.length ? `<details><summary>执行步骤</summary><ol>${combo.steps.map((step) => `<li>${escapeHTML(step)}</li>`).join('')}</ol></details>` : ''}
    </article>`).join('');
}

function renderRecommendations(recommendations, keywords) {
  const section = document.querySelector('#recommendation-section');
  const container = document.querySelector('#recommendation-list');
  section.hidden = !recommendations.length;
  document.querySelector('#recommendation-keywords').textContent = keywords.length ? `EDHREC 主题：${keywords.join(' · ')}` : '';
  container.innerHTML = recommendations.map((group, index) => `
    <section class="recommendation-group is-collapsed" data-tag="${escapeHTML(group.tag || '')}" data-group-index="${index}">
      <div class="recommendation-group-heading toggleable" role="button" tabindex="0" data-recommendation-toggle="${index}">
        <div><h3>${escapeHTML(group.header || 'Recommendations')}</h3><span>${group.cards?.length || 0} 张推荐</span></div>
        <button type="button" class="section-heading-toggle" aria-label="展开">展开</button>
      </div>
      <div class="recommendation-row">${(group.cards || []).map((item) => {
        const fills = (item.fills || []).map((fill) => `<li><strong>${escapeHTML(fill.label)} · 还缺 ${Number(fill.gap) || 0}</strong><span>${escapeHTML(fill.reason || '')}</span></li>`).join('');
        return `<article class="recommendation-card">
          ${renderCard({ card: item.card, quantity: 1, editor: false })}
          <div class="recommendation-meta">
            <div><span>Synergy</span><strong>${(Number(item.synergy || 0) * 100).toFixed(1)}%</strong></div>
            <div><span>Inclusion</span><strong>${(Number(item.inclusion_rate || 0) * 100).toFixed(1)}%</strong></div>
          </div>
          ${fills ? `<ul class="recommendation-fills">${fills}</ul>` : ''}
          <button class="swap-start ghost-button" type="button" data-add-name="${escapeHTML(item.card?.name || '')}">加入构筑</button>
          <p>${escapeHTML(item.reason || '')}</p>
          <a href="${escapeHTML(item.source_url || 'https://edhrec.com/')}" target="_blank" rel="noopener noreferrer">在 EDHREC 查看 ↗</a>
        </article>`;
      }).join('')}</div>
    </section>`).join('');
  
  // Attach toggle handlers for each recommendation group
  container.querySelectorAll('[data-recommendation-toggle]').forEach((toggle) => {
    toggle.addEventListener('click', handleRecommendationToggle);
    toggle.addEventListener('keydown', (e) => {
      if (e.key === 'Enter' || e.key === ' ') {
        e.preventDefault();
        handleRecommendationToggle.call(toggle, e);
      }
    });
  });
}

function renderDeckCards(cards) {
  const section = document.querySelector('#decklist-section');
  const container = document.querySelector('#deck-card-list');
  section.hidden = !cards.length;
  const groups = [
    ['Commander', cards.filter((item) => item.commander)],
    ['Nonlands', cards.filter((item) => !item.commander && !item.land)],
    ['Lands', cards.filter((item) => !item.commander && item.land)]
  ];
  container.innerHTML = groups.filter(([, items]) => items.length).map(([title, items]) => `
    <section class="deck-group"><h3>${title} <span>${items.reduce((sum, item) => sum + item.quantity, 0)}</span></h3><div class="card-grid">${items.map(renderCard).join('')}</div></section>`).join('') + 
    '<div class="deck-card-list-expand-overlay" data-decklist-expand>点击展开完整牌表 ▾</div>' +
    '<button type="button" class="deck-card-list-collapse-button" data-decklist-collapse>收起牌表 ▴</button>';
}

// --- light deck editor --------------------------------------------------------

// Begin editing the given deck. The cards array becomes the mutable editor state;
// invoke at the start of each edit session (after an analysis or a version load).
function beginEditing(sourceId, cards) {
  activeSourceId = String(sourceId || '');
  editorCards = cards.map((item) => ({ ...item }));
  editorUndo = [];
  editorRedo = [];
  editorDirty = false;
  syncEditorUI();
}

// A cheap structural clone sufficient for our flat card objects (card payloads are
// already plain JSON from the API). Keeps a real snapshot, not a shared ref.
function cloneCards(cards) {
  return cards.map((item) => ({ ...item }));
}

function pushUndo() {
  editorUndo.push(cloneCards(editorCards));
  if (editorUndo.length > 100) editorUndo.shift();
  editorRedo = [];
}

function markDirty() {
  editorDirty = true;
  editorDirtyNote.hidden = false;
}

function syncEditorUI() {
  editorUndoButton.disabled = editorUndo.length === 0;
  editorRedoButton.disabled = editorRedo.length === 0;
  editorDirtyNote.hidden = !editorDirty;
}

function indexPath(name) {
  return editorCards.findIndex((item) => String(item.card?.name || '').toLowerCase() === name.toLowerCase());
}

function isCommanderByName(name) {
  return editorCards.some((item) => item.commander && String(item.card?.name || '').toLowerCase() === name.toLowerCase());
}

function addCardToEditor(name) {
  const existing = indexPath(name);
  if (existing >= 0) {
    editorCards[existing].quantity += 1;
  } else {
    editorCards.push({ card: { name }, quantity: 1, commander: false, land: false });
  }
}

function removeCardFromEditor(name) {
  const index = indexPath(name);
  if (index < 0) return;
  const item = editorCards[index];
  if (item.quantity > 1) {
    item.quantity -= 1;
  } else {
    editorCards.splice(index, 1);
  }
}

// Apply one mutating operation with undo support, then re-render the deck list.
function applyEdit(mutate) {
  pushUndo();
  mutate(editorCards);
  editorRedo = [];
  markDirty();
  renderDeckCards(editorCards);
  syncEditorUI();
}

function undoEdit() {
  if (!editorUndo.length) return;
  editorRedo.push(cloneCards(editorCards));
  editorCards = editorUndo.pop();
  markDirty();
  renderDeckCards(editorCards);
  syncEditorUI();
}

function redoEdit() {
  if (!editorRedo.length) return;
  editorUndo.push(cloneCards(editorCards));
  editorCards = editorRedo.pop();
  markDirty();
  renderDeckCards(editorCards);
  syncEditorUI();
}

// Add a card by name. Resolves the Scryfall payload via the single-card endpoint so
// the new row gets its art/type/color identity; falls back to a bare name row and a
// message if the lookup fails, rather than blocking the local edit.
async function addCardByName(rawName) {
  const name = (rawName || '').trim();
  if (!name) return;
  let card = { name };
  try {
    const response = await fetch(`/api/v1/card?name=${encodeURIComponent(name)}`);
    if (response.ok) {
      const payload = await response.json();
      if (payload?.card?.name) {
        card = payload.card;
        card.land = String(payload.card.type_line || '').toLowerCase().includes('land');
      }
    }
  } catch {
    // Offline / transient failure: keep the bare name row.
  }
  applyEdit((cards) => {
    const existing = cards.findIndex((item) => String(item.card?.name || '').toLowerCase() === name.toLowerCase());
    if (existing >= 0) {
      cards[existing].quantity += 1;
    } else {
      cards.push({ card, quantity: 1, commander: false, land: Boolean(card.land) });
    }
  });
}

// Serialize the current editor state to the canonical decklist format. Uses the same
// "quantity name" shape PrintPlainText consumes; sorting is skipped to preserve the
// user's working order, which is fine for an editable local draft.
function editorToDeckText() {
  const commanders = editorCards.filter((item) => item.commander).map((item) => `${item.quantity || 1} ${item.card?.name || ''}`);
  const mainboard = editorCards.filter((item) => !item.commander).map((item) => `${item.quantity || 1} ${item.card?.name || ''}`);
  return `Commander\n${commanders.join('\n')}\n\nDeck\n${mainboard.join('\n')}`;
}

function downloadText(filename, text) {
  const blob = new Blob([text], { type: 'text/plain;charset=utf-8' });
  const url = URL.createObjectURL(blob);
  const link = document.createElement('a');
  link.href = url;
  link.download = filename;
  document.body.appendChild(link);
  link.click();
  link.remove();
  URL.revokeObjectURL(url);
}

function exportEditedDeck() {
  downloadText('decklist.txt', editorToDeckText());
}

function versionsKey() {
  return activeSourceId ? `${EDITOR_VERSIONS_KEY}.${activeSourceId}` : EDITOR_VERSIONS_KEY;
}

function loadVersions() {
  try {
    return JSON.parse(localStorage.getItem(versionsKey()) || '[]');
  } catch {
    return [];
  }
}

function saveVersions(versions) {
  try {
    localStorage.setItem(versionsKey(), JSON.stringify(versions));
  } catch {
    // Quota / disabled storage: versions are best-effort.
  }
}

function saveVersion() {
  const versions = loadVersions();
  versions.unshift({
    name: document.querySelector('#deck-name').textContent || 'Untitled deck',
    savedAt: new Date().toISOString(),
    cards: cloneCards(editorCards)
  });
  if (versions.length > 20) versions.length = 20;
  saveVersions(versions);
  renderVersionsList();
}

function renderVersionsList() {
  const versions = loadVersions();
  if (!versions.length) {
    editorVersionsList.innerHTML = '<p class="editor-empty">还没有保存的版本。</p>';
    return;
  }
  editorVersionsList.innerHTML = versions.map((version, index) => `
    <div class="editor-version">
      <div><strong>${escapeHTML(version.name || '未命名')}</strong><small>${new Date(version.savedAt).toLocaleString('zh-CN')} · ${(version.cards || []).reduce((sum, item) => sum + (item.quantity || 0), 0)} 张</small></div>
      <div class="editor-version-actions">
        <button type="button" data-load-version="${index}">载入</button>
        <button type="button" data-delete-version="${index}">删除</button>
      </div>
    </div>`).join('');
}

function loadVersion(index) {
  const versions = loadVersions();
  const version = versions[index];
  if (!version?.cards) return;
  beginEditing(activeSourceId, version.cards);
  markDirty();
  renderDeckCards(editorCards);
  editorVersionsPanel.hidden = true;
}

function deleteVersion(index) {
  const versions = loadVersions();
  versions.splice(index, 1);
  saveVersions(versions);
  renderVersionsList();
}

// Wire the editor toolbar and hotkeys. Deck-list card action buttons are delegated
// through the global click listener above; add/remove/undo/redo live here.
editorAddButton.addEventListener('click', () => addCardByName(editorAddInput.value));
editorAddInput.addEventListener('keydown', (event) => {
  if (event.key === 'Enter') {
    event.preventDefault();
    addCardByName(editorAddInput.value);
  }
});
editorUndoButton.addEventListener('click', undoEdit);
editorRedoButton.addEventListener('click', redoEdit);
editorSaveVersionButton.addEventListener('click', saveVersion);
editorExportButton.addEventListener('click', exportEditedDeck);
editorVersionsButton.addEventListener('click', () => {
  editorVersionsPanel.hidden = !editorVersionsPanel.hidden;
  renderVersionsList();
});

function renderCard(item) {
  const card = item.card || {};
  const picturedFaces = Array.isArray(card.faces) ? card.faces.filter((face) => face.image_small || face.image_normal) : [];
  const image = card.image_small || card.image_normal || picturedFaces[0]?.image_small || picturedFaces[0]?.image_normal;
  const previewImage = card.image_normal || card.image_small || picturedFaces[0]?.image_normal || picturedFaces[0]?.image_small;
  const faceSwitch = picturedFaces.length > 1 ? `<div class="card-faces">${picturedFaces.map((face, index) => `<button type="button" data-face="${index}" class="${index === 0 ? 'active' : ''}" aria-pressed="${index === 0}">${index === 0 ? '正面' : '反面'}</button>`).join('')}</div>` : '';
  const faceData = picturedFaces.length > 1 ? ` data-faces="${escapeHTML(JSON.stringify(picturedFaces))}"` : '';
  const editorControls = item.editor === false ? '' : `<div class="card-edit-controls">
    <button type="button" data-card-subtract="${escapeHTML(card.name || '')}" aria-label="减少 ${escapeHTML(card.name || '')}"><span aria-hidden="true">−</span></button>
    <button type="button" data-card-add="${escapeHTML(card.name || '')}" aria-label="增加 ${escapeHTML(card.name || '')}"><span aria-hidden="true">+</span></button>
  </div>`;
  return `<div class="mtg-card"${faceData} data-preview-src="${escapeHTML(previewImage || '')}" data-preview-name="${escapeHTML(card.name || '')}" data-card-text="${escapeHTML(previewTextForCard(card))}">
    <div class="mtg-card-face" aria-describedby="card-preview">${image ? `<img loading="lazy" src="${escapeHTML(image)}" alt="${escapeHTML(card.name)}">` : '<div class="card-placeholder"></div>'}<span class="card-quantity">${item.quantity || 1}×</span><strong>${escapeHTML(card.name || 'Unknown card')}</strong></div>
    ${faceSwitch}
    ${editorControls}
  </div>`;
}

swapSearch.addEventListener('input', () => renderSwapRemoveList(swapSearch.value));
swapSubmit.addEventListener('click', compareSwap);
document.addEventListener('keydown', (event) => {
  if (event.key === 'Escape' && !swapModal.hidden) closeSwap();
  if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === 'z') {
    if (event.shiftKey) {
      event.preventDefault();
      redoEdit();
    } else {
      event.preventDefault();
      undoEdit();
    }
  }
});

document.addEventListener('click', (event) => {
  const subtractButton = event.target.closest('[data-card-subtract]');
  if (subtractButton) {
    const name = subtractButton.dataset.cardSubtract || '';
    const item = editorCards.find((entry) => String(entry.card?.name || '').toLowerCase() === name.toLowerCase());
    if (item?.commander) return;
    applyEdit((cards) => removeCardFromEditor(name));
    return;
  }
  const addButton = event.target.closest('[data-card-add]');
  if (addButton) {
    const name = addButton.dataset.cardAdd || '';
    if (addButton.closest('.deck-group') && isCommanderByName(name)) return;
    applyEdit((cards) => addCardToEditor(name));
    return;
  }
  const loadVersionButton = event.target.closest('[data-load-version]');
  if (loadVersionButton) {
    loadVersion(Number(loadVersionButton.dataset.loadVersion));
    return;
  }
  const deleteVersionButton = event.target.closest('[data-delete-version]');
  if (deleteVersionButton) {
    deleteVersion(Number(deleteVersionButton.dataset.deleteVersion));
    return;
  }
  const swapStart = event.target.closest('.swap-start');
  if (swapStart) {
    openSwap(swapStart.dataset.addName || '');
    return;
  }
  const removeOption = event.target.closest('.swap-remove-option');
  if (removeOption) {
    selectedSwapRemove = removeOption.dataset.removeName || '';
    swapSubmit.disabled = !selectedSwapRemove;
    renderSwapRemoveList(swapSearch.value);
    return;
  }
  if (event.target.closest('[data-swap-close]') || event.target.closest('#swap-close')) {
    closeSwap();
    return;
  }
  const button = event.target.closest('.card-faces button');
  if (!button) return;
  event.preventDefault();
  const cardElement = button.closest('.mtg-card');
  let faces;
  try { faces = JSON.parse(cardElement.dataset.faces || '[]'); } catch { return; }
  const face = faces[Number(button.dataset.face)];
  if (!face) return;
  const imageElement = cardElement.querySelector('img');
  imageElement?.setAttribute('src', face.image_small || face.image_normal || '');
  imageElement?.setAttribute('alt', face.name || '');
  cardElement.dataset.previewSrc = face.image_normal || face.image_small || '';
  cardElement.dataset.previewName = face.name || '';
  cardElement.querySelector('.mtg-card-face strong').textContent = face.name || '';
  cardElement.dataset.cardText = previewTextForCard({ name: face.name, mana_cost: face.mana_cost, type_line: face.type_line, oracle_text: face.oracle_text });
  cardElement.querySelectorAll('.card-faces button').forEach((item) => {
    const active = item === button;
    item.classList.toggle('active', active);
    item.setAttribute('aria-pressed', String(active));
  });
  if (activePreviewCard === cardElement) showCardPreview(cardElement);
});

const preview = document.querySelector('#card-preview');
const previewImage = document.querySelector('#card-preview-image');
const previewName = document.querySelector('#card-preview-name');
const previewOracle = document.querySelector('#card-preview-oracle');
const canHover = window.matchMedia('(hover: hover) and (pointer: fine)');
let activePreviewCard = null;
let previewImageCache = new Map(); // url -> resolved element (pre-loaded large art)

// Compose the hover preview's text block from a card's type line and rules text.
// For multi-faced cards, each face's type line and oracle are joined so a split/DFC
// card shows both halves. The front-end builds this from the payload already served
// by the API, so no extra lookup happens on hover.
// When a Chinese translation exists (chinese_type_line / chinese_oracle_text), it is
// shown below the English text so the full card list and the builder hover agree.
function previewTextFor(card) {
  const cardObj = card || {};
  const lines = [];
  const addFace = (face) => {
    const type = String(face?.type_line || '').trim();
    const oracle = String(face?.oracle_text || '').trim();
    const zhType = String(face?.chinese_type_line || '').trim();
    const zhOracle = String(face?.chinese_oracle_text || '').trim();
    if (type) lines.push(type);
    if (zhType && zhType !== type) lines.push(zhType);
    if (oracle) lines.push(oracle);
    if (zhOracle && zhOracle !== oracle) lines.push(zhOracle);
  };
  addFace(cardObj);
  for (const face of Array.isArray(cardObj.faces) ? cardObj.faces : []) {
    addFace(face);
  }
  return lines.join('\n\n');
}

// Alias kept for the decklist card renderer, which uses the longer name.
function previewTextForCard(card) {
  return previewTextFor(card);
}

// The hover preview's text block. Same content as the full decklist cards, but
// the builder flow keeps it in the same place so both show the Chinese oracle.
function builderPreviewTextFor(card) {
  return previewTextFor(card);
}

function showCardPreview(trigger) {
  if (!canHover.matches || !trigger) return;
  const card = trigger.closest('[data-preview-src]');
  const source = card?.dataset.previewSrc;
  // The chosen-list rows have no <img> of their own, so "small" would be empty and the
  // preview would stay blank until the large art loads. When no thumbnail exists, fall
  // back to the large art directly.
  if (!source) return;
  activePreviewCard = card;
  previewImage.alt = card.dataset.previewName || '';
  previewName.textContent = card.dataset.previewName || '';
  // Show the oracle text (type line + rules) read from the hovered card's own
  // data, so a hover can answer "what does this card do" without a click.
  previewOracle.textContent = card.dataset.cardText || '';
  previewOracle.hidden = !previewOracle.textContent;
  // Show the small art immediately (it is already on the page and hot), then swap in
  // the large art once it has finished loading, so the preview never hangs blank.
  const small = card.querySelector('img')?.src || '';
  const cached = previewImageCache.get(source);
  if (cached) {
    previewImage.src = cached.src;
  } else if (small) {
    previewImage.src = small;
    const img = new Image();
    img.onload = () => {
      if (activePreviewCard === card) previewImage.src = source;
      previewImageCache.set(source, img);
    };
    img.onerror = () => previewImageCache.delete(source);
    img.src = source;
  } else {
    // No on-page thumbnail (e.g. the chosen-list rows): load the large art directly.
    previewImage.src = source;
  }
  preview.hidden = false;
  requestAnimationFrame(() => positionCardPreview(trigger));
}

function positionCardPreview(trigger) {
  if (preview.hidden) return;
  const gap = 12;
  const anchor = trigger.getBoundingClientRect();
  const box = preview.getBoundingClientRect();
  let left = anchor.right + gap;
  if (left + box.width > window.innerWidth - gap) left = anchor.left - box.width - gap;
  left = Math.max(gap, Math.min(left, window.innerWidth - box.width - gap));
  const top = Math.max(gap, Math.min(anchor.top, window.innerHeight - box.height - gap));
  preview.style.left = `${left}px`;
  preview.style.top = `${top}px`;
}

function hideCardPreview() {
  activePreviewCard = null;
  preview.hidden = true;
  previewImage.removeAttribute('src');
  previewOracle.textContent = '';
  previewOracle.hidden = true;
}

document.addEventListener('pointerover', (event) => {
  const trigger = event.target.closest('.mtg-card, .builder-candidate, .builder-chosen-item, .builder-commander-box-item, .builder-commander-preview img');
  if (trigger && !trigger.contains(event.relatedTarget)) showCardPreview(trigger);
});
document.addEventListener('pointerout', (event) => {
  const trigger = event.target.closest('.mtg-card, .builder-candidate, .builder-chosen-item, .builder-commander-box-item, .builder-commander-preview img');
  if (trigger && !trigger.contains(event.relatedTarget)) hideCardPreview();
});
document.addEventListener('focusin', (event) => { if (event.target.matches('.mtg-card, .builder-candidate, .builder-chosen-item, .builder-commander-box-item, .builder-commander-preview img')) showCardPreview(event.target); });
document.addEventListener('focusout', (event) => { if (event.target.matches('.mtg-card, .builder-candidate, .builder-chosen-item, .builder-commander-box-item, .builder-commander-preview img')) hideCardPreview(); });
document.addEventListener('keydown', (event) => { if (event.key === 'Escape') hideCardPreview(); });
window.addEventListener('scroll', hideCardPreview, { passive: true });
window.addEventListener('resize', hideCardPreview);

function renderProvider(prefix, provider, secondaryMetrics) {
  const status = document.querySelector(`#${prefix}-status`);
  const content = document.querySelector(`#${prefix}-content`);
  if (!provider || provider.status !== 'success') {
    const code = provider?.error?.code || '';
    const message = provider?.error?.message || '该评分网站暂时无法返回结果。';
    if (!provider || provider.status === 'unavailable') {
      // "unavailable" is a known, expected state (e.g. text input has no Moxfield
      // URL for CommanderSalt), so label it differently from a hard failure.
      status.textContent = '跳过';
      status.className = 'status muted';
      content.innerHTML = `<p class="provider-muted">${escapeHTML(message)}</p>`;
    } else {
      status.textContent = '失败';
      status.className = 'status error';
      content.innerHTML = `<p class="provider-error">${escapeHTML(message)}</p>`;
    }
    return;
  }

  status.textContent = '完成';
  status.className = 'status success';
  const metrics = provider.metrics || {};
  const power = formatNumber(metrics.power_level, 2);
  const list = secondaryMetrics
    .filter(([key]) => metrics[key] !== undefined)
    .map(([key, label]) => `<div class="metric"><small>${label}</small><strong>${formatMetric(key, metrics[key])}</strong></div>`)
    .join('');
  const brackets = renderBrackets(metrics);
  const suggestions = prefix === 'salt'
    ? renderSuggestions(metrics.suggestions)
    : renderEDHBracketDetails(metrics.bracket_details);
  content.innerHTML = `
    <div class="hero-metric"><strong>${power}</strong><span>/ 10 Power Level</span></div>
    ${brackets}
    <div class="metric-list">${list}</div>
    ${suggestions}`;
}

function renderEDHBracketDetails(details) {
  if (!details || typeof details !== 'object') return '';
  const reasons = Array.isArray(details.rules_bracket_reasons)
    ? details.rules_bracket_reasons.map((reason) => String(reason ?? '').trim()).filter(Boolean)
    : [];
  const evaluatedReason = String(details.evaluated_bracket_reason ?? '')
    .replace(/(Recommended Bracket:\s*\d+)/gi, '$1\n')
    .replace(/(Minimum Bracket:\s*\d+)/gi, '$1\n')
    .replace(/\.\s+(?=[A-Z])/g, '.\n')
    .trim();
  const counters = [
    ['Game Changers', Number(details.game_changers) || 0, details.game_changer_names || []],
    ['Early 2-Card Combos', Number(details.early_2_card_combos) || 0, details.early_2_card_combo_names || []],
    ['Extra Turns', Number(details.extra_turns) || 0, details.extra_turn_names || []],
    ['Mass Land Denial', Number(details.mass_land_denial) || 0, details.mass_land_denial_names || []]
  ];
  const counterChips = counters.map(([label, value]) => `<span class="bracket-counter"><strong>${value}</strong><small>${escapeHTML(label)}</small></span>`).join('');
  const enumerations = counters
    .filter(([, , names]) => Array.isArray(names) && names.length)
    .map(([label, , names]) => {
      const list = names.map((name) => `<li>${escapeHTML(name)}</li>`).join('');
      return `<div class="bracket-enum"><span>${escapeHTML(label)}</span><ul>${list}</ul></div>`;
    }).join('');
  if (!reasons.length && !evaluatedReason && !counterChips && !enumerations) return '';
  const rows = reasons.map((reason) => {
    const [title, description] = splitEDHReason(reason);
    return `
      <article class="suggestion-item">
        <div class="suggestion-item-title"><strong>${escapeHTML(title)}</strong></div>
        ${description ? `<p>${escapeHTML(description)}</p>` : ''}
      </article>`;
  }).join('');
  return `
    <section class="suggestions edh-bracket-details">
      <div class="suggestions-title"><span>BRACKET DETAILS</span><strong>基础 Bracket 判定依据</strong></div>
      ${counterChips ? `<div class="bracket-counters">${counterChips}</div>` : ''}
      ${enumerations ? `<div class="bracket-enumerations">${enumerations}</div>` : ''}
      ${rows ? `<div class="suggestion-list">${rows}</div>` : ''}
      ${evaluatedReason ? `<p class="suggestions-summary">${escapeHTML(evaluatedReason)}</p>` : ''}
    </section>`;
}

function splitEDHReason(reason) {
  const normalized = String(reason ?? '').replace(/\s+/g, ' ').trim();
  const separator = normalized.indexOf(' - ');
  if (separator < 0) return [normalized, ''];
  return [normalized.slice(0, separator), normalized.slice(separator + 3)];
}

function renderSuggestions(suggestions) {
  if (!suggestions || typeof suggestions !== 'object') return '';
  // Groups that start collapsed: their contents are long and rarely read up front.
  // The heading stays clickable to expand on demand.
  const collapseByDefault = new Set(['rationale', 'harden']);
  const groups = [
    ['rule_zero', '对局前说明', '在开始游戏前值得与牌桌沟通'],
    ['rationale', '当前档位原因', 'CommanderSalt 对当前强度判断的依据'],
    ['soften', '降低强度建议', '让牌组更适合较低强度环境'],
    ['harden', '提高强度建议', '进一步提升速度、稳定性或威胁']
  ];
  const renderedGroups = groups.map(([key, title, description]) => {
    const items = Array.isArray(suggestions[key]) ? suggestions[key] : [];
    if (!items.length) return '';
    const rows = items.map(renderSuggestionItem).filter(Boolean).join('');
    if (!rows) return '';
    const collapsed = collapseByDefault.has(key);
    return `
      <section class="suggestion-group suggestion-${key.replace('_', '-')}${collapsed ? ' is-collapsed' : ''}" data-suggestion-group="${key}">
        <button type="button" class="suggestion-heading" data-suggestion-toggle aria-expanded="${!collapsed}">
          <strong>${title}</strong><small>${description}</small><span class="suggestion-heading-chevron">▾</span>
        </button>
        <div class="suggestion-body${collapsed ? '' : ''}">
          <div class="suggestion-list">${rows}</div>
        </div>
      </section>`;
  }).filter(Boolean).join('');
  const summary = String(suggestions.summary ?? '').trim();
  if (!renderedGroups && !summary) return '';
  return `
    <section class="suggestions">
      <div class="suggestions-title"><span>DECK SUGGESTIONS</span><strong>CommanderSalt 建议</strong></div>
      ${summary ? `<p class="suggestions-summary">${escapeHTML(summary)}</p>` : ''}
      ${renderedGroups}
    </section>`;
}

function renderSuggestionItem(item) {
  if (!item || typeof item !== 'object') return '';
  const title = String(item.label || humanizeID(item.id) || '').trim();
  const why = String(item.why ?? '').trim();
  if (!title && !why) return '';
  const sentiment = ['caution', 'warning'].includes(String(item.sentiment).toLowerCase()) ? ' caution' : '';
  const direction = item.direction === 'up' ? '↑' : item.direction === 'down' ? '↓' : '';
  return `
    <article class="suggestion-item${sentiment}">
      <div class="suggestion-item-title"><strong>${escapeHTML(title || '建议')}</strong>${direction ? `<span>${direction}</span>` : ''}</div>
      ${why ? `<p>${escapeHTML(why)}</p>` : ''}
    </article>`;
}

function humanizeID(value) {
  return String(value ?? '')
    .replace(/[_-]+/g, ' ')
    .replace(/([a-z0-9])([A-Z])/g, '$1 $2')
    .replace(/^./, (char) => char.toUpperCase());
}

function renderBrackets(metrics) {
  const rules = metrics.rules_bracket;
  const evaluated = metrics.evaluated_bracket;
  if (rules === undefined && evaluated === undefined) return '';
  return `
    <div class="bracket-pair">
      <div class="bracket-value">
        <span class="bracket-number">${formatNumber(rules, 0)}</span>
        <div><strong>规则 Bracket</strong><small>按官方卡牌限制得到的最低档位</small></div>
      </div>
      <div class="bracket-value evaluated">
        <span class="bracket-number">${formatNumber(evaluated, 0)}</span>
        <div><strong>评估 Bracket</strong><small>结合整副牌强度后的建议档位</small></div>
      </div>
    </div>`;
}

function formatMetric(key, value) {
  if (key === 'average_playability') return `${formatNumber(value, 1)}%`;
  return formatNumber(value, key === 'salt' || key === 'impact' ? 1 : 2);
}

function formatNumber(value, digits) {
  const number = Number(value);
  if (!Number.isFinite(number)) return escapeHTML(String(value ?? '—'));
  return number.toLocaleString('zh-CN', { maximumFractionDigits: digits });
}

function escapeHTML(value) {
  return String(value ?? '').replace(/[&<>'"]/g, (char) => ({
    '&': '&amp;', '<': '&lt;', '>': '&gt;', "'": '&#39;', '"': '&quot;'
  })[char]);
}

// Scroll-triggered fade-in animation with hysteresis to prevent flicker
function initScrollAnimations() {
  const observer = new IntersectionObserver((entries) => {
    entries.forEach((entry) => {
      // Use a larger threshold to create a "dead zone" at boundaries
      // Only trigger state changes when element crosses 30% visibility
      const isVisible = entry.isIntersecting && entry.intersectionRatio >= 0.25;
      
      if (isVisible) {
        // Fade in when entering viewport (with sufficient overlap)
        entry.target.classList.add('fade-in-visible');
        entry.target.classList.remove('fade-out-visible', 'fade-in-hidden');
      } else if (entry.intersectionRatio < 0.1) {
        // Only fade out when almost completely out of view
        entry.target.classList.remove('fade-in-visible');
        entry.target.classList.add('fade-out-visible');
      }
      // Between 10% and 25%: do nothing (hysteresis zone to prevent flicker)
    });
  }, {
    threshold: [0, 0.1, 0.25, 0.5, 0.75, 1.0],  // Multiple thresholds for smooth detection
    rootMargin: '50px 0px 50px 0px'  // Extended margin for earlier detection
  });

  // Apply animations to major sections, but exclude recommendation section
  // (it contains many dynamic cards, better to keep it stable)
  document.querySelectorAll('.catalog-section:not(#recommendation-section), .result-card').forEach((el) => {
    el.classList.add('fade-in-hidden');
    observer.observe(el);
  });
}

function handleRecommendationToggle(e) {
  const toggle = e.currentTarget;
  const index = toggle.getAttribute('data-recommendation-toggle');
  const group = document.querySelector(`.recommendation-group[data-group-index="${index}"]`);
  const button = toggle.querySelector('.section-heading-toggle');
  
  if (!group) return;
  
  const isCollapsed = group.classList.contains('is-collapsed');
  
  if (isCollapsed) {
    // Expand and scroll into view with focus
    group.classList.remove('is-collapsed');
    button.textContent = '收起';
    button.setAttribute('aria-label', '收起');
    
    // Scroll the group into center of viewport
    setTimeout(() => {
      const rect = group.getBoundingClientRect();
      const scrollTarget = window.scrollY + rect.top - (window.innerHeight / 2) + (rect.height / 2);
      window.scrollTo({ top: scrollTarget, behavior: 'smooth' });
    }, 100);
  } else {
    // Collapse
    group.classList.add('is-collapsed');
    button.textContent = '展开';
    button.setAttribute('aria-label', '展开');
  }
}

function showStickyNav() {
  const nav = document.querySelector('#sticky-nav');
  nav.hidden = false;
  document.body.classList.add('has-sticky-nav');
  
  // Set up intersection observer for active link highlighting
  const sections = document.querySelectorAll('#results, #manabase-section, #construction-section, #combo-section, #recommendation-section, #decklist-section');
  const navLinks = document.querySelectorAll('.sticky-nav-link');
  
  const observer = new IntersectionObserver((entries) => {
    entries.forEach((entry) => {
      if (entry.isIntersecting) {
        const id = entry.target.id;
        navLinks.forEach((link) => {
          if (link.getAttribute('href') === `#${id}`) {
            link.classList.add('active');
          } else {
            link.classList.remove('active');
          }
        });
      }
    });
  }, {
    threshold: 0.3,
    rootMargin: '-80px 0px -60% 0px'
  });
  
  sections.forEach((section) => observer.observe(section));
  
  // Smooth scroll on nav click
  navLinks.forEach((link) => {
    link.addEventListener('click', (e) => {
      e.preventDefault();
      const targetId = link.getAttribute('href').substring(1);
      const target = document.getElementById(targetId);
      if (target) {
        const navHeight = nav.offsetHeight;
        const targetPosition = target.getBoundingClientRect().top + window.scrollY - navHeight - 20;
        window.scrollTo({ top: targetPosition, behavior: 'smooth' });
      }
    });
  });
}
