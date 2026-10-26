package spellbook

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const maxResponseSize = 6 << 20

// pageSize matches the API's own pagination (the "limit" query param is
// capped at 12 by the backend regardless of what we request).
const pageSize = 12

type Combo struct {
	ID              string
	Name            string
	Components      []Component
	Result          string
	Steps           []string
	SourceURL       string
	ManaValueNeeded int
	// RequiresTemplates holds the numeric template IDs of any "requires" entries
	// (the site's `Ee.requirements` list determines which are acceptable).
	RequiresTemplates []int
	// ProducesFeatureIDs holds the numeric feature IDs the combo produces; the site
	// compares these against a fixed "game-defining" producer list.
	ProducesFeatureIDs []int
	// BracketTag is Spellbook's own per-variant bracket classification letter
	// (live values include R/S/P/E; semantics are not officially documented, so
	// it is carried for diagnostics but not used in scoring decisions).
	BracketTag string
}

type Component struct {
	Name        string
	OracleID    string
	ImageNormal string
	ImageSmall  string
	Zone        string
}

type Client struct {
	baseURL    string
	httpClient *http.Client
	// cache retains each card-name query's payload for a short TTL. Spellbook is
	// the one provider in the analysis pipeline that is called once per deck card
	// name (up to ~24 queries per deck), and it rate-limits hard, so even a
	// minute-long cache collapses repeat lookups across decks and re-analyses.
	cache map[string]cacheEntry
	mu    sync.Mutex
	ttl   time.Duration
	// disk is the persistent tier under the same keying as the memory cache.
	// Fresh rows (diskTTL) serve without network; rows of any age are served
	// when the upstream fails, so a throttled or downed Spellbook degrades to
	// slightly-old combos instead of dropping a deck's early-combo detection.
	disk *diskCache
	// minNameDelay spaces out per-name requests. Spellbook throttles request
	// bursts (a rapid 12-request batch gets 429s and a ~45s window), while
	// paced serial requests stay at 200 — so pacing is what keeps a cold-cache
	// deck analysis from blanking every combo.
	minNameDelay time.Duration
	// lastFetch timestamps the most recent real name query so Search can pace
	// uncached lookups across the burst-throttling window.
	lastFetch time.Time
}

// SetDiskDir wires the persistent cache directory (e.g. config.CacheDir); rows
// live under <dir>/spellbook/. Best-effort: a broken directory just means the
// client keeps its memory-only behavior.
func (c *Client) SetDiskDir(dir string) {
	if sub, err := newDiskCache(dir); err == nil {
		c.mu.Lock()
		c.disk = sub
		c.mu.Unlock()
	}
}

type cacheEntry struct {
	combos    []Combo
	expiresAt time.Time
}

type response struct {
	Results []variant `json:"results"`
}

type variant struct {
	ID              string `json:"id"`
	Description     string `json:"description"`
	ManaValueNeeded int    `json:"manaValueNeeded"`
	BracketTag      string `json:"bracketTag"`
	Status          string `json:"status"`
	Uses            []struct {
		Card struct {
			Name                string `json:"name"`
			OracleID            string `json:"oracleId"`
			ImageUriFrontNormal string `json:"imageUriFrontNormal"`
			ImageUriFrontSmall  string `json:"imageUriFrontSmall"`
		} `json:"card"`
		ZoneLocations []string `json:"zoneLocations"`
	} `json:"uses"`
	Produces []struct {
		Feature struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
		} `json:"feature"`
	} `json:"produces"`
	Requires []struct {
		Quantity int `json:"quantity"`
		Template struct {
			ID int `json:"id"`
		} `json:"template"`
	} `json:"requires"`
}

// NewWithPacing builds a client whose per-name request spacing is configurable.
// Production keeps the 1.5s default (Spellbook burst-throttles), while test rigs
// pointed at a local stub can drop it to milliseconds so cold analyzes don't
// spend 12×1.5s pacing against an upstream that cannot throttle.
func NewWithPacing(baseURL string, httpClient *http.Client, minNameDelay time.Duration) *Client {
	if minNameDelay <= 0 {
		minNameDelay = 1500 * time.Millisecond
	}
	return &Client{
		baseURL:      strings.TrimRight(baseURL, "/"),
		httpClient:   httpClient,
		cache:        make(map[string]cacheEntry),
		ttl:          10 * time.Minute,
		minNameDelay: minNameDelay,
	}
}

func New(baseURL string, httpClient *http.Client) *Client {
	return NewWithPacing(baseURL, httpClient, 1500*time.Millisecond)
}

func (c *Client) Search(ctx context.Context, names []string, limit int) ([]Combo, error) {
	if limit < 1 {
		limit = 12
	}
	seen := make(map[string]struct{})
	var combos []Combo
	for _, name := range prioritizeNames(names) {
		key := normalizeSpellbookName(name)
		if cached, ok := c.get(key); ok {
			appendCached(cached, &combos, &seen, limit)
			continue
		}
		// 磁盘层新鲜数据（24h 内）直接采用并回填内存，绕开限速闸门。
		if fresh, ok := c.diskGetFresh(key); ok {
			c.set(key, fresh)
			appendCached(fresh, &combos, &seen, limit)
			continue
		}
		if err := c.waitSlot(ctx); err != nil {
			return combos, err
		}
		fetched, err := c.fetchName(ctx, key, limit)
		if err != nil {
			// 上游抖动/限流时优先退给磁盘里任何年龄的旧数据：组合库变化很慢，
			// 昨天的组合线远好过这次分析直接漏检。
			if stale, ok := c.diskGetAny(key); ok {
				c.set(key, stale)
				appendCached(stale, &combos, &seen, limit)
				continue
			}
			// 无旧数据可退：隔一个限速间隔再试一次，抗瞬时 5xx/网络抖动；
			// 仍然失败才放弃这个名字（与旧行为一致——批内其余名字继续）。
			if err := c.waitSlot(ctx); err != nil {
				return combos, err
			}
			if fetched, err = c.fetchName(ctx, key, limit); err != nil {
				continue
			}
		}
		c.set(key, fetched)
		c.diskPut(key, fetched)
		appendCached(fetched, &combos, &seen, limit)
	}
	return combos, nil
}

// waitSlot reserves the next pacing slot (reserveSlot) and sleeps until it
// opens, abandoning the wait when ctx is done.
func (c *Client) waitSlot(ctx context.Context) error {
	if wait := c.reserveSlot(); wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}
	return nil
}

// reserveSlot claims the next request slot under the pacing gate and returns how
// long the caller must wait before sending. Both the main loop and the retry
// path go through it, so a retry can never burst past the throttle window.
func (c *Client) reserveSlot() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	wait := time.Duration(0)
	if last := c.lastFetch; !last.IsZero() {
		if gap := c.minNameDelay - time.Since(last); gap > 0 {
			wait = gap
		}
	}
	c.lastFetch = time.Now().Add(wait)
	return wait
}

// fetchName queries Spellbook for one card name and re-fetches pages until the
// caller's limit is reached, the API stops returning results, or the page cap
// is hit. The cap matters because q is a substring search: a name whose only
// matches are noise (q=silence → Deafening Silence, Stony Silence, …) would
// otherwise page through the API's entire noise set looking for exact matches.
func (c *Client) fetchName(ctx context.Context, name string, limit int) ([]Combo, error) {
	var combos []Combo
	for page := 0; page < maxPagesPerName; page++ {
		pageCombos, more, err := c.fetchPage(ctx, name, limit, page*pageSize)
		if err != nil {
			return combos, err
		}
		combos = append(combos, pageCombos...)
		if len(combos) >= limit || !more {
			break
		}
	}
	return combos, nil
}

// maxPagesPerName bounds pagination for one card-name query. Exact-name matches
// dominate the first page or two of Spellbook's ranking, so four pages cover
// prolific combo cards while capping noise paging at ~6 paced requests.
const maxPagesPerName = 4

// fetchPage fetches one offset page of variants for a name and returns the
// 2-card combos found on it that actually use the queried card (3+ card combos
// are skipped, and so is substring-search noise — see variantUsesCard).
// more reports whether the page was full, i.e. the API likely has more results
// worth fetching.
func (c *Client) fetchPage(ctx context.Context, name string, limit, offset int) ([]Combo, bool, error) {
	endpoint, _ := url.Parse(c.baseURL + "/variants/")
	query := endpoint.Query()
	// Query the front face: spellbook's search doesn't recognize "X // Y".
	query.Set("q", name)
	query.Set("limit", "12")
	if offset > 0 {
		query.Set("offset", strconv.Itoa(offset))
	}
	endpoint.RawQuery = query.Encode()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "PowerLevelAggregator/0.2")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, false, err
	}
	data, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize+1))
	resp.Body.Close()
	if readErr != nil {
		return nil, false, readErr
	}
	if resp.StatusCode != http.StatusOK {
		return nil, false, &rateLimitError{status: resp.StatusCode}
	}
	var payload response
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, false, err
	}
	var combos []Combo
	for _, item := range payload.Results {
		if len(combos) >= limit {
			break
		}
		if len(item.Uses) > 2 {
			continue
		}
		// q is a substring search over card names, not an exact lookup:
		// querying a deck's "Silence" returns Deafening Silence and Stony
		// Silence combos whose components are all absent from the deck. Only
		// variants that use the queried card itself are this deck's combos.
		if !variantUsesCard(item, name) {
			continue
		}
		combo := Combo{ID: item.ID, SourceURL: "https://commanderspellbook.com/combo/" + item.ID, ManaValueNeeded: item.ManaValueNeeded, BracketTag: item.BracketTag}
		for _, use := range item.Uses {
			zone := ""
			if len(use.ZoneLocations) > 0 {
				zone = strings.Join(use.ZoneLocations, ",")
			}
			combo.Components = append(combo.Components, Component{Name: use.Card.Name, OracleID: use.Card.OracleID, ImageNormal: use.Card.ImageUriFrontNormal, ImageSmall: use.Card.ImageUriFrontSmall, Zone: zone})
		}
		for _, req := range item.Requires {
			combo.RequiresTemplates = append(combo.RequiresTemplates, req.Template.ID)
		}
		for _, p := range item.Produces {
			if p.Feature.ID != 0 {
				combo.ProducesFeatureIDs = append(combo.ProducesFeatureIDs, p.Feature.ID)
			}
		}
		parts := make([]string, 0, len(combo.Components))
		for _, part := range combo.Components {
			parts = append(parts, part.Name)
		}
		combo.Name = strings.Join(parts, " + ")
		produces := make([]string, 0, len(item.Produces))
		for _, p := range item.Produces {
			if p.Feature.Name != "" {
				produces = append(produces, p.Feature.Name)
			}
		}
		combo.Result = strings.Join(produces, ", ")
		for _, step := range strings.Split(item.Description, "\n") {
			if step = strings.TrimSpace(step); step != "" {
				combo.Steps = append(combo.Steps, step)
			}
		}
		combos = append(combos, combo)
	}
	// The page was full (12 results) and the limit is not yet reached: ask for
	// the next page so 2-card win-cons hidden behind 3-card combo walls surface.
	more := len(payload.Results) >= 12 && len(combos) < limit
	return combos, more, nil
}

// rateLimitError marks a throttled (429) or otherwise failed name query.
type rateLimitError struct {
	status int
}

func (e *rateLimitError) Error() string {
	return "spellbook request failed"
}

// appendCached merges a cached or freshly-fetched combo list into the running
// result, deduplicating by combo ID and stopping at the caller's limit.
func appendCached(source []Combo, combos *[]Combo, seen *map[string]struct{}, limit int) {
	if len(*combos) >= limit {
		return
	}
	for _, combo := range source {
		if _, ok := (*seen)[combo.ID]; ok {
			continue
		}
		(*seen)[combo.ID] = struct{}{}
		*combos = append(*combos, combo)
		if len(*combos) >= limit {
			return
		}
	}
}

func (c *Client) get(key string) ([]Combo, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.cache[key]
	if !ok || time.Now().After(entry.expiresAt) {
		return nil, false
	}
	return entry.combos, true
}

func (c *Client) set(key string, combos []Combo) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cache[key] = cacheEntry{combos: combos, expiresAt: time.Now().Add(c.ttl)}
}

// --- on-disk cache (mirrors edhpowerlevel's getcards disk tier) ----------------
//
// One JSON file per normalized card name under <cacheDir>/spellbook/. File
// names are the SHA-256 of the key so unicode and path separators never reach
// the filesystem. All operations are best-effort: a missing or corrupt row
// simply falls back to the network path. Empty combo lists are cached too —
// most cards are not combo pieces, and negative rows are what keep cold decks
// from re-paging the upstream forever.

// diskTTL is how long a disk row is trusted without refresh. The combo DB
// changes slowly, so a day keeps early-combo detection fresh while making
// repeat and overlapping deck analyses fully offline.
const diskTTL = 24 * time.Hour

type diskEntry struct {
	FetchedAt time.Time `json:"fetched_at"`
	Combos    []Combo   `json:"combos"`
}

type diskCache struct{ dir string }

func newDiskCache(base string) (*diskCache, error) {
	dir := filepath.Join(base, "spellbook")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &diskCache{dir: dir}, nil
}

func (d *diskCache) path(key string) string {
	sum := sha256.Sum256([]byte(key))
	return filepath.Join(d.dir, hex.EncodeToString(sum[:])+".json")
}

// get returns the row for key, its fetch time, and whether a usable row was
// found. A row with a zero fetch time is treated as corrupt and dropped.
func (d *diskCache) get(key string) ([]Combo, time.Time, bool) {
	if d == nil {
		return nil, time.Time{}, false
	}
	data, err := os.ReadFile(d.path(key))
	if err != nil {
		return nil, time.Time{}, false
	}
	var entry diskEntry
	if err := json.Unmarshal(data, &entry); err != nil || entry.FetchedAt.IsZero() {
		_ = os.Remove(d.path(key))
		return nil, time.Time{}, false
	}
	return entry.Combos, entry.FetchedAt, true
}

// put writes the row atomically (temp file + rename) so a crash never leaves a
// half-written entry behind.
func (d *diskCache) put(key string, combos []Combo, fetchedAt time.Time) {
	if d == nil {
		return
	}
	data, err := json.Marshal(diskEntry{FetchedAt: fetchedAt, Combos: combos})
	if err != nil {
		return
	}
	temp, err := os.CreateTemp(d.dir, "*.tmp")
	if err != nil {
		return
	}
	name := temp.Name()
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		_ = os.Remove(name)
		return
	}
	if err := temp.Close(); err != nil {
		_ = os.Remove(name)
		return
	}
	_ = os.Rename(name, d.path(key))
}

func (c *Client) diskGetFresh(key string) ([]Combo, bool) {
	c.mu.Lock()
	disk := c.disk
	c.mu.Unlock()
	if disk == nil {
		return nil, false
	}
	combos, fetchedAt, ok := disk.get(key)
	if !ok || time.Since(fetchedAt) >= diskTTL {
		return nil, false
	}
	return combos, true
}

func (c *Client) diskGetAny(key string) ([]Combo, bool) {
	c.mu.Lock()
	disk := c.disk
	c.mu.Unlock()
	if disk == nil {
		return nil, false
	}
	combos, _, ok := disk.get(key)
	return combos, ok
}

func (c *Client) diskPut(key string, combos []Combo) {
	c.mu.Lock()
	disk := c.disk
	c.mu.Unlock()
	if disk == nil {
		return
	}
	disk.put(key, combos, time.Now())
}

func prioritizeNames(names []string) []string {
	keywords := []string{"twinflame", "dualcaster", "ghostly flicker", "naru meha", "dramatic reversal", "isochron", "thassa", "consultation", "tainted pact", "underworld breach", "lion's eye diamond"}
	var priority, rest []string
	for _, name := range names {
		lower := strings.ToLower(name)
		matched := false
		for _, keyword := range keywords {
			if strings.Contains(lower, keyword) {
				matched = true
				break
			}
		}
		if matched {
			priority = append(priority, name)
		} else {
			rest = append(rest, name)
		}
	}
	priority = append(priority, rest...)
	if len(priority) > 24 {
		priority = priority[:24]
	}
	return priority
}

// variantUsesCard reports whether one of the variant's components is the
// queried card (front-face, case-insensitive). It is what turns Spellbook's
// substring q search into an exact per-card lookup.
func variantUsesCard(item variant, key string) bool {
	for _, use := range item.Uses {
		if normalizeSpellbookName(use.Card.Name) == key {
			return true
		}
	}
	return false
}

// allComponentsInDeck reports whether every component of the variant is in the
// deck-name list (front-face, case-insensitive).
func allComponentsInDeck(item variant, deckNames []string) bool {
	set := make(map[string]struct{}, len(deckNames))
	for _, name := range deckNames {
		// Index by front-face name so a deck entry "X // Y" matches both "X // Y"
		// and "X" (and vice versa) from the combo API. Display names are untouched.
		set[normalizeSpellbookName(name)] = struct{}{}
	}
	if len(item.Uses) < 2 {
		return false
	}
	for _, use := range item.Uses {
		if _, ok := set[normalizeSpellbookName(use.Card.Name)]; !ok {
			return false
		}
	}
	return true
}

// normalizeSpellbookName lowercases and collapses a name to its front face for
// matching; it does not affect any returned display/image data.
func normalizeSpellbookName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	if index := strings.Index(name, " // "); index > 0 {
		name = strings.TrimSpace(name[:index])
	}
	return name
}
