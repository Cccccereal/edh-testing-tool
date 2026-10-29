package cardcatalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// 中文译名索引：Scryfall 的 autocomplete 只认英文片段，搜索框要支持中文译名
//（Scryfall/mtgch 一脉的官方简中译名），就地在本地建一份 zh → 英文规范名的映射，
// 供 autocomplete 输入中文时反查。索引按 client TTL 过期，落盘成单文件让重启保温。

const (
	// lang:zhs 的 unique=cards 结果约 2.4 万条，175 条/页 ≈ 140 页；页数上限是
	// 防御性的，防止查询语法变更后变成无限翻页。
	zhIndexMaxPages = 400
	// 共享节拍器控制出页节奏：~3.3 req/s 匀速无突发（Scryfall 对突发即便均值
	// 不高也会 429），全量索引约 1.5–2.5 分钟，后台一次性成本。
	zhIndexWorkers        = 4
	zhIndexRequestSpacing = 300 * time.Millisecond
)

type zhIndex struct {
	BuiltAt time.Time         `json:"built_at"`
	Entries map[string]string `json:"entries"` // 简中译名（双面牌含各面）→ 英文规范名
	// ByEn 是展示用的反向映射（英文规范名 → 简中译名），建议列表在英文名后
	// 补中文用。双面牌多个译名时取字典序最小的一个，保证同牌跨重启展示稳定。
	ByEn map[string]string `json:"by_en,omitempty"`
}

// zhIndexState 挂在 Client 上：后台构建 + 单飞。构建独立于请求上下文（一次请求
// 取消不该撕掉整份索引），触发请求只等完成信号。
type zhIndexState struct {
	mu       sync.Mutex
	loaded   bool
	index    zhIndex
	building bool
	done     chan struct{}
	buildErr error
}

// ChineseSuggest 反查中文译名，返回按匹配度排序的英文规范名（与 autocomplete
// 同形，调用方照旧走 Lookup / 合法性过滤）。
func (c *Client) ChineseSuggest(ctx context.Context, query string, limit int) ([]string, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, errors.New("chinese suggest query is empty")
	}
	if limit <= 0 {
		limit = 12
	}
	idx, err := c.loadZhIndex(ctx)
	if err != nil {
		return nil, err
	}
	return matchChinese(idx.Entries, query, limit), nil
}

// loadZhIndex 返回未过期的索引；内存没有就先读落盘文件，再没有就后台构建并等它。
// 构建失败时若内存里还有过期旧索引，降级返回旧数据（比让中文搜索整体不可用好）。
func (c *Client) loadZhIndex(ctx context.Context) (zhIndex, error) {
	c.zh.mu.Lock()
	if c.zh.loaded && time.Since(c.zh.index.BuiltAt) < c.ttl {
		idx := c.zh.index
		c.zh.mu.Unlock()
		return idx, nil
	}
	path := c.zhIndexPath()
	if path != "" {
		if idx, ok := readZhIndex(path, c.ttl); ok {
			c.zh.index = idx
			c.zh.loaded = true
			c.zh.mu.Unlock()
			return idx, nil
		}
	}
	if !c.zh.building {
		c.zh.building = true
		c.zh.done = make(chan struct{})
		c.zh.buildErr = nil
		go func() {
			started := time.Now()
			idx, err := c.buildZhIndex(context.Background())
			c.zh.mu.Lock()
			c.zh.building = false
			if err == nil {
				c.zh.index = idx
				c.zh.loaded = true
				if path := c.zhIndexPath(); path != "" {
					// 落盘失败只损失下次启动的预热，不影响本次结果
					_ = writeZhIndex(path, idx)
				}
			} else {
				c.zh.buildErr = err
			}
			close(c.zh.done)
			c.zh.mu.Unlock()
			slog.Info("zh index build finished", "entries", len(idx.Entries), "duration", time.Since(started).Truncate(time.Millisecond), "err", err)
		}()
	}
	done := c.zh.done
	c.zh.mu.Unlock()

	select {
	case <-done:
	case <-ctx.Done():
		return zhIndex{}, ctx.Err()
	}
	c.zh.mu.Lock()
	defer c.zh.mu.Unlock()
	if c.zh.loaded {
		return c.zh.index, nil
	}
	return zhIndex{}, c.zh.buildErr
}

func (c *Client) buildZhIndex(ctx context.Context) (zhIndex, error) {
	base := url.Values{}
	base.Set("q", "lang:zhs")
	base.Set("unique", "cards")
	base.Set("order", "name")

	entries := make(map[string]string, 24000)
	var mu sync.Mutex
	var firstErr error
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	fail := func(err error) {
		mu.Lock()
		if firstErr == nil {
			firstErr = err
		}
		mu.Unlock()
		cancel()
	}
	collect := func(raw scryfallCard) {
		if raw.Name == "" {
			return
		}
		// 双面牌的 printed_name 在各面上，逐面收集，都指回同一张英文牌
		mu.Lock()
		defer mu.Unlock()
		collectZhName(entries, raw.PrintedName, raw.Name)
		for _, face := range raw.CardFaces {
			collectZhName(entries, face.PrintedName, raw.Name)
		}
	}

	// 第 1 页同步抓：顺便确认查询有效，失败直接短路
	values := url.Values{}
	for k, vs := range base {
		values[k] = vs
	}
	values.Set("page", "1")
	first, _, err := c.fetchSearchPage(ctx, c.baseURL+"/cards/search?"+values.Encode())
	if err != nil {
		return zhIndex{}, fmt.Errorf("build zh index page 1: %w", err)
	}
	for _, raw := range first {
		collect(raw)
	}

	// 页号从 2 起由工作协程动态认领；谁抓到没有下一页的页，就把号发完。
	// 出请求的节奏由共享节拍器统一控制（匀速、无突发），Scryfall 对突发
	// 即便均值不高也会回 429。
	var nextPage int64 = 2
	var exhausted int32
	var wg sync.WaitGroup
	limiter := time.NewTicker(zhIndexRequestSpacing)
	defer limiter.Stop()
	for w := 0; w < zhIndexWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				if atomic.LoadInt32(&exhausted) != 0 || ctx.Err() != nil {
					return
				}
				page := int(atomic.AddInt64(&nextPage, 1))
				if page > zhIndexMaxPages {
					return
				}
				select {
				case <-limiter.C:
				case <-ctx.Done():
					return
				}
				values := url.Values{}
				for k, vs := range base {
					values[k] = vs
				}
				values.Set("page", strconv.Itoa(page))
				var data []scryfallCard
				var pageHasMore bool
				var err error
				// 构建在后台跑，等得起：429/5xx 按 10s→…→60s 退避后原页重试
				//（doWithRetry 的 Retry-After 被 5s 封顶，而惩罚窗往往更长，
				// 只有真的等够时长才能出来）。
				for attempt := 1; attempt <= 12; attempt++ {
					data, pageHasMore, err = c.fetchSearchPage(ctx, c.baseURL+"/cards/search?"+values.Encode())
					if err == nil || ctx.Err() != nil {
						break
					}
					if strings.Contains(err.Error(), "HTTP 422") {
						// 末页之后 Scryfall 用 422 表示「页码不存在」：这就是翻完了
						atomic.StoreInt32(&exhausted, 1)
						return
					}
					if strings.Contains(err.Error(), "HTTP 400") || strings.Contains(err.Error(), "HTTP 404") {
						break // 查询本身错误，等待无济于事
					}
					backoff := time.Duration(min(attempt, 6)) * 10 * time.Second
					slog.Warn("zh index page fetch failed, backing off", "page", page, "attempt", attempt, "backoff", backoff.String(), "err", err)
					select {
					case <-time.After(backoff):
					case <-ctx.Done():
						return
					}
					// 等待期间别的协程可能已经确认翻完，别再空转
					if atomic.LoadInt32(&exhausted) != 0 {
						return
					}
				}
				if err != nil {
					// 一页失败就放弃整轮：半份索引反而造成「搜不到」的假象
					fail(fmt.Errorf("build zh index page %d: %w", page, err))
					return
				}
				for _, raw := range data {
					collect(raw)
				}
				if !pageHasMore {
					atomic.StoreInt32(&exhausted, 1)
				}
			}
		}()
	}
	wg.Wait()
	mu.Lock()
	count := len(entries)
	mu.Unlock()
	if firstErr != nil {
		return zhIndex{}, firstErr
	}
	if count == 0 {
		return zhIndex{}, errors.New("zh index build produced no entries")
	}
	idx := zhIndex{BuiltAt: time.Now(), Entries: entries, ByEn: deriveByEn(entries)}
	return idx, nil
}

// deriveByEn 从 zh→en 映射推导展示用反查表；同牌多译名取字典序最小者，
// 让展示与构建/载入顺序无关。
func deriveByEn(entries map[string]string) map[string]string {
	keys := make([]string, 0, len(entries))
	for zh := range entries {
		keys = append(keys, zh)
	}
	sort.Strings(keys)
	byEn := make(map[string]string, len(entries))
	for _, zh := range keys {
		en := entries[zh]
		if _, ok := byEn[en]; !ok {
			byEn[en] = zh
		}
	}
	return byEn
}

// ChineseAlias 返回建议条目要展示的简中译名；索引未就绪或该卡无译名则空串。
// 不为展示阻塞调用方，但会顺手在后台把索引拉起来——首次英文查询没有后缀，
// 后续键击就有了。
func (c *Client) ChineseAlias(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	c.zh.mu.Lock()
	loaded := c.zh.loaded
	c.zh.mu.Unlock()
	if !loaded {
		go func() {
			_, _ = c.loadZhIndex(context.Background())
		}()
		return ""
	}
	c.zh.mu.Lock()
	defer c.zh.mu.Unlock()
	return c.zh.index.ByEn[name]
}

func collectZhName(entries map[string]string, zh, en string) {
	zh = strings.TrimSpace(zh)
	if zh == "" {
		return
	}
	entries[zh] = en
}

// matchChinese 按「完全相等 > 前缀 > 包含 > 子序列」打分；同一张牌取最高分，
// 分数相同按英文名稳定排序。子序列即乱序模糊：漏字、隔字都能命中。
func matchChinese(entries map[string]string, query string, limit int) []string {
	q := strings.ToLower(strings.TrimSpace(query))
	best := make(map[string]int, 16)
	for zh, en := range entries {
		z := strings.ToLower(zh)
		var score int
		switch {
		case z == q:
			score = 100
		case strings.HasPrefix(z, q):
			score = 80
		case strings.Contains(z, q):
			score = 60
		case isSubsequence(z, q):
			score = 40
		default:
			continue
		}
		if score > best[en] {
			best[en] = score
		}
	}
	type hit struct {
		name  string
		score int
	}
	hits := make([]hit, 0, len(best))
	for name, score := range best {
		hits = append(hits, hit{name: name, score: score})
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score > hits[j].score
		}
		return hits[i].name < hits[j].name
	})
	if len(hits) > limit {
		hits = hits[:limit]
	}
	out := make([]string, 0, len(hits))
	for _, h := range hits {
		out = append(out, h.name)
	}
	return out
}

func isSubsequence(s, sub string) bool {
	rs := []rune(sub)
	if len(rs) == 0 {
		return false
	}
	i := 0
	for _, r := range s {
		if r == rs[i] {
			i++
			if i == len(rs) {
				return true
			}
		}
	}
	return false
}

// 索引文件放在卡牌磁盘缓存目录的上一级（zh-index.json），与 per-card 缓存同根。

func (c *Client) zhIndexPath() string {
	if c.disk == nil {
		return ""
	}
	return filepath.Join(filepath.Dir(c.disk.dir), "zh-index.json")
}

func readZhIndex(path string, ttl time.Duration) (zhIndex, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return zhIndex{}, false
	}
	var idx zhIndex
	if err := json.Unmarshal(data, &idx); err != nil || len(idx.Entries) == 0 {
		return zhIndex{}, false
	}
	if time.Since(idx.BuiltAt) >= ttl {
		return zhIndex{}, false
	}
	// 旧格式文件没有 by_en 字段：现场推导，避免为展示格式整轮重抓
	if len(idx.ByEn) == 0 {
		idx.ByEn = deriveByEn(idx.Entries)
	}
	return idx, true
}

func writeZhIndex(path string, idx zhIndex) error {
	data, err := json.Marshal(idx)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
