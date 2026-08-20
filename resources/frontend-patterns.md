# 前端开发模式总结

本文档总结了 Commander Power Check 项目中实现的现代化前端设计模式，适合在类似项目中复用。

## 1. 浅色/深色主题切换系统

### 核心原理
使用 CSS 变量 + 根元素类名切换的方式实现主题系统，所有颜色通过变量定义，便于全局管理。

### 实现要点

**CSS 变量定义：**
```css
:root {
  /* 暗色模式（默认） */
  --ink: #f4f2e9;
  --muted: #9fa095;
  --surface: #1a1c18;
  --line: #3a3d36;
  --acid: #d7ff3f;
  --blue: #7eb8ff;
  --orange: #ff9259;
}

:root.light-mode {
  /* 浅色模式 */
  --ink: #1a1c18;
  --muted: #666b60;
  --surface: #f4f5f2;
  --line: #d1d4cc;
  --acid: #6b8a00;
  --blue: #1a5bb8;
  --orange: #d9511c;
}
```

**主题切换 JavaScript：**
```javascript
const themeToggle = document.querySelector('#theme-toggle');

themeToggle.addEventListener('click', () => {
  const root = document.documentElement;
  const isLight = root.classList.toggle('light-mode');
  themeToggle.textContent = isLight ? '亮色' : '暗色';
  localStorage.setItem('theme', isLight ? 'light' : 'dark');
});

// 页面加载时恢复用户偏好
if (localStorage.getItem('theme') === 'light') {
  document.documentElement.classList.add('light-mode');
  themeToggle.textContent = '亮色';
}
```

**HTML 结构：**
```html
<button id="theme-toggle" class="theme-toggle" type="button" aria-label="切换主题">暗色</button>
```

**按钮样式：**
```css
.theme-toggle {
  position: fixed;
  top: 80px;
  right: 24px;
  z-index: 101;
  padding: 10px 16px;
  background: var(--surface);
  border: 1px solid var(--line);
  color: var(--ink);
  font-size: 13px;
  font-weight: 600;
  transition: all .2s ease;
}

.theme-toggle:hover {
  background: var(--acid);
  color: #10110f;
  transform: translateY(-2px);
  box-shadow: 0 4px 12px rgba(0,0,0,.2);
}
```

### 适配硬编码颜色
对于使用硬编码颜色的元素，需要为浅色模式单独添加覆盖样式：

```css
/* 暗色模式（默认） */
.card { background: #191b17; }

/* 浅色模式覆盖 */
:root.light-mode .card { background: #e4e6e2; }
```

### 系统性适配清单
确保以下元素都适配了两种模式：
- ✅ 页面背景和渐变
- ✅ 文字颜色（主文字、次要文字、链接）
- ✅ 卡片和面板背景
- ✅ 输入框和表单元素
- ✅ 按钮的默认和悬停状态
- ✅ 边框和分隔线
- ✅ 阴影效果
- ✅ 骨架屏加载动画
- ✅ 进度条和图表背景
- ✅ 下拉菜单和弹出层

## 2. 粘性导航栏（Sticky Navigation）

### 功能特点
- 页面滚动时固定在顶部
- 半透明背景 + 毛玻璃效果
- 根据滚动位置高亮当前区域的链接
- 平滑滚动跳转

### HTML 结构
```html
<nav id="sticky-nav" class="sticky-nav" hidden>
  <div class="sticky-nav-inner">
    <a href="#results" class="sticky-nav-link">结果</a>
    <a href="#manabase-section" class="sticky-nav-link">法力基础</a>
    <a href="#combo-section" class="sticky-nav-link">Combo</a>
  </div>
</nav>
```

### CSS 样式
```css
.sticky-nav {
  position: fixed;
  top: 0;
  left: 0;
  right: 0;
  z-index: 100;
  background: rgba(16, 17, 15, 0.95);
  backdrop-filter: blur(12px);
  border-bottom: 1px solid var(--line);
}

:root.light-mode .sticky-nav {
  background: rgba(250, 251, 249, 0.95);
}

.sticky-nav-inner {
  max-width: 1400px;
  margin: 0 auto;
  padding: 0 24px;
  display: flex;
  gap: 2px;
  overflow-x: auto;
}

.sticky-nav-link {
  padding: 14px 18px;
  color: var(--muted);
  font-size: 13px;
  font-weight: 600;
  text-decoration: none;
  white-space: nowrap;
  transition: color .2s, background .2s;
}

.sticky-nav-link:hover {
  color: var(--ink);
  background: rgba(215, 255, 63, 0.08);
}

.sticky-nav-link.active {
  color: var(--acid);
  background: rgba(215, 255, 63, 0.12);
}
```

### JavaScript 实现
```javascript
const stickyNav = document.querySelector('#sticky-nav');
const sections = document.querySelectorAll('[id$="-section"]');
const navLinks = document.querySelectorAll('.sticky-nav-link');

// 显示/隐藏导航栏
function updateStickyNav() {
  const results = document.querySelector('#results');
  if (!results) return;
  
  const rect = results.getBoundingClientRect();
  stickyNav.hidden = rect.top > 0;
}

// 高亮当前区域链接
function updateActiveLink() {
  let activeSection = null;
  
  sections.forEach(section => {
    const rect = section.getBoundingClientRect();
    if (rect.top <= 100 && rect.bottom > 100) {
      activeSection = section;
    }
  });
  
  navLinks.forEach(link => {
    const href = link.getAttribute('href');
    if (activeSection && href === `#${activeSection.id}`) {
      link.classList.add('active');
    } else {
      link.classList.remove('active');
    }
  });
}

// 平滑滚动
navLinks.forEach(link => {
  link.addEventListener('click', (e) => {
    e.preventDefault();
    const targetId = link.getAttribute('href').slice(1);
    const target = document.getElementById(targetId);
    if (target) {
      target.scrollIntoView({ behavior: 'smooth', block: 'start' });
    }
  });
});

// 监听滚动事件
let ticking = false;
window.addEventListener('scroll', () => {
  if (!ticking) {
    window.requestAnimationFrame(() => {
      updateStickyNav();
      updateActiveLink();
      ticking = false;
    });
    ticking = true;
  }
});
```

## 3. 滚动触发动画（Scroll-triggered Animations）

### 功能说明
使用 Intersection Observer API 实现元素进入视口时的淡入动画，性能优于传统的滚动事件监听。

### CSS 动画定义
```css
@keyframes fadeInUp {
  from {
    opacity: 0;
    transform: translateY(60px);
  }
  to {
    opacity: 1;
    transform: translateY(0);
  }
}

.fade-in-hidden {
  opacity: 0;
  transform: translateY(60px);
}

.fade-in-visible {
  animation: fadeInUp 0.8s ease-out forwards;
}

/* 支持用户偏好：减少动画 */
@media (prefers-reduced-motion: reduce) {
  .fade-in-hidden {
    opacity: 1;
    transform: none;
  }
  .fade-in-visible {
    animation: none;
  }
}
```

### JavaScript 实现
```javascript
// 配置观察器
const observerOptions = {
  root: null,           // 使用视口作为根
  rootMargin: '0px',    // 无边距
  threshold: 0.15       // 元素 15% 可见时触发
};

const observer = new IntersectionObserver((entries) => {
  entries.forEach(entry => {
    if (entry.isIntersecting) {
      entry.target.classList.remove('fade-in-hidden');
      entry.target.classList.add('fade-in-visible');
      // 触发后停止观察，避免重复动画
      observer.unobserve(entry.target);
    }
  });
}, observerOptions);

// 为需要动画的元素添加初始类并开始观察
function setupScrollAnimations() {
  const elements = document.querySelectorAll('.result-card, .section-title');
  elements.forEach(el => {
    el.classList.add('fade-in-hidden');
    observer.observe(el);
  });
}

// 在内容加载后调用
setupScrollAnimations();
```

### 使用建议
- `threshold` 设置在 0.1-0.2 之间效果最佳
- 避免同时观察过多元素（>100 个）
- 触发后立即 `unobserve()` 以释放资源
- 必须添加 `prefers-reduced-motion` 支持，尊重用户的无障碍偏好

## 4. 骨架屏加载状态

### 功能特点
在内容加载时显示动画骨架，比纯加载图标更友好。

### CSS 实现
```css
.skeleton {
  background: linear-gradient(
    90deg,
    #1c1e1a 25%,
    #252822 50%,
    #1c1e1a 75%
  );
  background-size: 200% 100%;
  animation: skeleton-shimmer 1.5s ease-in-out infinite;
  border-radius: 4px;
}

:root.light-mode .skeleton {
  background: linear-gradient(
    90deg,
    #e3e5e1 25%,
    #d8dbd5 50%,
    #e3e5e1 75%
  );
}

@keyframes skeleton-shimmer {
  0% { background-position: 200% 0; }
  100% { background-position: -200% 0; }
}

.skeleton-text { height: 16px; margin-bottom: 8px; }
.skeleton-heading { height: 32px; width: 60%; margin-bottom: 16px; }
.skeleton-metric { height: 80px; margin-bottom: 12px; }
```

### HTML 使用
```html
<div class="skeleton skeleton-heading"></div>
<div class="skeleton skeleton-text"></div>
<div class="skeleton skeleton-text" style="width: 80%;"></div>
<div class="skeleton skeleton-metric"></div>
```

## 5. 可折叠区域（Collapsible Sections）

### 功能特点
- 默认折叠状态，节省空间
- 点击标题展开/折叠
- 展开时自动滚动到视图中心

### HTML 结构
```html
<details class="collapsible-section">
  <summary>
    <h3>区域标题</h3>
    <span class="chevron">▼</span>
  </summary>
  <div class="collapsible-content">
    <!-- 内容 -->
  </div>
</details>
```

### CSS 样式
```css
.collapsible-section {
  border: 1px solid var(--line);
  background: var(--surface);
  margin-bottom: 16px;
}

.collapsible-section summary {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 16px 20px;
  cursor: pointer;
  list-style: none;
  user-select: none;
}

.collapsible-section summary::-webkit-details-marker {
  display: none;
}

.chevron {
  transition: transform 0.2s;
}

.collapsible-section[open] .chevron {
  transform: rotate(180deg);
}

.collapsible-content {
  padding: 0 20px 20px;
}
```

### JavaScript 增强
```javascript
document.querySelectorAll('.collapsible-section').forEach(details => {
  details.addEventListener('toggle', () => {
    if (details.open) {
      // 展开时滚动到视图中心
      setTimeout(() => {
        details.scrollIntoView({
          behavior: 'smooth',
          block: 'center'
        });
      }, 100);
    }
  });
});
```

## 6. 卡片悬停微交互

### CSS 实现
```css
.card {
  transition: transform 0.2s ease, box-shadow 0.2s ease;
}

.card:hover {
  transform: translateY(-4px);
  box-shadow: 0 12px 24px rgba(0, 0, 0, 0.2);
}

:root.light-mode .card:hover {
  box-shadow: 0 12px 24px rgba(0, 0, 0, 0.1);
}

/* 按钮发光效果 */
.button {
  transition: filter 0.2s, transform 0.15s;
}

.button:hover {
  filter: brightness(1.1);
  transform: scale(1.02);
}
```

## 7. 响应式布局要点

### 移动端适配
```css
@media (max-width: 720px) {
  .shell {
    width: min(100% - 24px, 1120px);
    padding-top: 42px;
  }
  
  .hero {
    margin-bottom: 38px;
  }
  
  h1 {
    font-size: 34px;
  }
  
  .sticky-nav-inner {
    overflow-x: auto;
    -webkit-overflow-scrolling: touch;
  }
}
```

### 触摸设备适配
```css
/* 移除触摸设备上的悬停预览 */
@media (hover: none), (pointer: coarse) {
  .card-preview {
    display: none !important;
  }
}
```

## 8. 性能优化技巧

### 使用 requestAnimationFrame 处理滚动
```javascript
let ticking = false;

window.addEventListener('scroll', () => {
  if (!ticking) {
    window.requestAnimationFrame(() => {
      handleScroll();
      ticking = false;
    });
    ticking = true;
  }
});
```

### 图片懒加载
```html
<img loading="lazy" src="image.jpg" alt="描述">
```

### 字体加载优化
```css
:root {
  --font-body: "Inter", system-ui, sans-serif;
  --font-ui: "Inter", system-ui, sans-serif;
}

/* 避免字体闪烁 */
body {
  font-family: var(--font-body);
  font-display: swap;
}
```

## 9. 无障碍支持

### 必须包含的属性
```html
<!-- 按钮的 aria-label -->
<button aria-label="切换主题">☀️</button>

<!-- 图片的 alt 文本 -->
<img src="card.jpg" alt="Lightning Bolt 卡牌">

<!-- 表单的 label -->
<label for="deck-url">牌组链接</label>
<input id="deck-url" type="url">
```

### 键盘导航支持
```css
/* 聚焦样式 */
:focus-visible {
  outline: 2px solid var(--acid);
  outline-offset: 2px;
}

button:focus-visible,
a:focus-visible {
  outline: 2px solid var(--acid);
}
```

### 动画偏好支持
```css
@media (prefers-reduced-motion: reduce) {
  * {
    animation-duration: 0.01ms !important;
    animation-iteration-count: 1 !important;
    transition-duration: 0.01ms !important;
  }
}
```

## 总结

以上模式组合使用可以快速构建现代化、用户友好的 Web 界面。核心原则：

1. **主题系统**：使用 CSS 变量 + 类名切换
2. **交互反馈**：微交互、动画、状态提示
3. **性能优先**：懒加载、requestAnimationFrame、Intersection Observer
4. **无障碍**：语义化 HTML、ARIA 属性、键盘导航、动画偏好
5. **渐进增强**：从基本功能开始，逐步增强体验

这些模式可以独立使用，也可以组合复用到其他项目中。
