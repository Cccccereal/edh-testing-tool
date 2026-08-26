package spellbook

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
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
	// minNameDelay spaces out per-name requests. Spellbook throttles request
	// bursts (a rapid 12-request batch gets 429s and a ~45s window), while
	// paced serial requests stay at 200 — so pacing is what keeps a cold-cache
	// deck analysis from blanking every combo.
	minNameDelay time.Duration
	// lastFetch timestamps the most recent real name query so Search can pace
	// uncached lookups across the burst-throttling window.
	lastFetch time.Time
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

func New(baseURL string, httpClient *http.Client) *Client {
	return &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		httpClient: httpClient,
		// Spellbook is per-card-name, so a generous TTL is safe: combo data does
		// not change minute-to-minute, and it only shrinks the request volume.
		cache:        make(map[string]cacheEntry),
		ttl:          10 * time.Minute,
		minNameDelay: 1500 * time.Millisecond,
	}
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
		// Pace uncached names so a cold deck analysis does not burst the API.
		// Cached names skip the wait: only real network hits need spacing.
		delay := c.minNameDelay
		if last := c.lastFetch; !last.IsZero() {
			if wait := delay - time.Since(last); wait > 0 {
				timer := time.NewTimer(wait)
				select {
				case <-ctx.Done():
					timer.Stop()
					return combos, ctx.Err()
				case <-timer.C:
				}
			}
		}
		c.lastFetch = time.Now()
		fetched, err := c.fetchName(ctx, key, limit)
		if err != nil {
			// A throttled or failed single name is skipped: the batch continues
			// with the remaining names rather than blanking every combo.
			continue
		}
		c.set(key, fetched)
		appendCached(fetched, &combos, &seen, limit)
	}
	return combos, nil
}

// fetchName queries Spellbook for one card name and re-fetches pages until the
// caller's limit is reached or the API stops returning new 2-card combos.
func (c *Client) fetchName(ctx context.Context, name string, limit int) ([]Combo, error) {
	var combos []Combo
	for offset := 0; ; offset += pageSize {
		page, more, err := c.fetchPage(ctx, name, limit, offset)
		if err != nil {
			return combos, err
		}
		combos = append(combos, page...)
		if len(combos) >= limit || !more {
			break
		}
	}
	return combos, nil
}

// fetchPage fetches one offset page of variants for a name and returns the
// 2-card combos found on it (3+ card combos are skipped — see fetchName).
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
		combo := Combo{ID: item.ID, SourceURL: "https://commanderspellbook.com/combo/" + item.ID, ManaValueNeeded: item.ManaValueNeeded}
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
