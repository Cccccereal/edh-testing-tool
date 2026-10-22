package cardcatalog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	maxBatchSize = 75
	// notFoundFallbackLimit caps how many collection-endpoint not_found names one
	// batch retried via /cards/named; see fetchBatch.
	notFoundFallbackLimit = 15
	maxResponseSize       = 8 << 20
	userAgent             = "PowerLevelAggregator/0.2"
)

type CardFace struct {
	Name        string `json:"name"`
	PrintedName string `json:"printed_name,omitempty"`
	ManaCost    string `json:"mana_cost,omitempty"`
	TypeLine    string `json:"type_line,omitempty"`
	OracleText  string `json:"oracle_text,omitempty"`
	// ChineseTypeLine / ChineseOracleText carry the printed (localized) type
	// line and rules text of the zhs printing for multi-faced cards, so the
	// hover preview can show both halves of a DFC in Chinese.
	ChineseTypeLine   string `json:"chinese_type_line,omitempty"`
	ChineseOracleText string `json:"chinese_oracle_text,omitempty"`
	ImageNormal       string `json:"image_normal,omitempty"`
	ImageSmall        string `json:"image_small,omitempty"`
	// GameChanger mirrors Scryfall's per-face `game_changer` flag (some entries are
	// flagged on an individual face of a multi-face card).
	GameChanger bool `json:"game_changer,omitempty"`
	// Power is the face's printed power ("1", "2+", "*", or "" for non-creatures).
	Power string `json:"power,omitempty"`
}

type Card struct {
	OracleID      string            `json:"oracle_id,omitempty"`
	Name          string            `json:"name"`
	PrintedName   string            `json:"printed_name,omitempty"`
	ManaCost      string            `json:"mana_cost,omitempty"`
	TypeLine      string            `json:"type_line,omitempty"`
	OracleText    string            `json:"oracle_text,omitempty"`
	ColorIdentity []string          `json:"color_identity,omitempty"`
	Keywords      []string          `json:"keywords,omitempty"`
	Legalities    map[string]string `json:"legalities,omitempty"`
	ImageNormal   string            `json:"image_normal,omitempty"`
	ImageSmall    string            `json:"image_small,omitempty"`
	Layout        string            `json:"layout,omitempty"`
	Faces         []CardFace        `json:"faces,omitempty"`
	Cmc           float64           `json:"cmc,omitempty"`
	ProducedMana  []string          `json:"produced_mana,omitempty"`
	Power         string            `json:"power,omitempty"`
	// Rank is Scryfall's edhrec_rank (1 = most popular). The Scryfall-backed
	// gap backfill sorts by it so cold candidates still surface community-
	// proven cards first; 0 means "not ranked" and sorts last.
	Rank int `json:"edhrec_rank,omitempty"`
	// GameChanger mirrors Scryfall's official `game_changer` field (the Commander
	// Game Changers list). It is the live source of truth for bracket counting —
	// the list changes every few months and a hardcoded snapshot has already gone
	// stale once, so consumers must prefer this field over any name list.
	GameChanger bool `json:"game_changer,omitempty"`
	// Localized (Simplified Chinese) payloads, populated when Scryfall serves a "zhs"
	// variant. Empty when no translation exists for the card.
	ChineseName       string `json:"chinese_name,omitempty"`
	ChineseTypeLine   string `json:"chinese_type_line,omitempty"`
	ChineseOracleText string `json:"chinese_oracle_text,omitempty"`
}

// ManaValue returns the card's converted mana cost (Scryfall cmc). The manabase
// classifier overrides this for multi-faced cards, where the front face's printed
// cost is the castable cost.
func (c Card) ManaValue() float64 { return c.Cmc }

// Produced returns the card's produced_mana letters (empty for fetchlands, which
// tap for no mana directly).
func (c Card) Produced() []string { return c.ProducedMana }

// Search returns cards matching an arbitrary Scryfall query string (their syntax,
// e.g. "ci:wub legal:commander (t:creature or t:artifact)"), ordered by Scryfall's
// default relevance. It pages through the result set up to `limit` cards (a caller
// asking for more than one page continues fetching with the `page` parameter). Used
// by the deck builder as a gap-filling source when EDHREC's recommendation pool runs
// dry, so a draft can keep growing beyond the ~30 cards EDHREC suggests.
func (c *Client) Search(ctx context.Context, query string, limit int) ([]Card, error) {
	if strings.TrimSpace(query) == "" {
		return nil, errors.New("Scryfall search query is empty")
	}
	if limit <= 0 {
		limit = maxBatchSize
	}
	if limit > 700 {
		limit = 700
	}
	values := url.Values{}
	values.Set("q", query)
	values.Set("unique", "prints")
	values.Set("order", "edhrec")
	values.Set("dir", "desc")

	cards := make([]Card, 0, limit)
	page := 1
	for len(cards) < limit && page <= 40 {
		values.Set("page", strconv.Itoa(page))
		endpoint := c.baseURL + "/cards/search?" + values.Encode()
		data, hasMore, err := c.fetchSearchPage(ctx, endpoint)
		if err != nil {
			return cards, err
		}
		for _, raw := range data {
			card := normalizeCard(raw)
			key := normalizeName(card.Name)
			c.mu.Lock()
			c.cache[key] = cacheEntry{card: card, expiresAt: time.Now().Add(c.ttl)}
			c.mu.Unlock()
			c.disk.put(key, card, time.Now())
			cards = append(cards, card)
			if len(cards) >= limit {
				return cards, nil
			}
		}
		if !hasMore {
			break
		}
		page++
	}
	return cards, nil
}

// fetchSearchPage fetches one page of Scryfall /cards/search results and reports
// whether another page follows.
func (c *Client) fetchSearchPage(ctx context.Context, endpoint string) ([]scryfallCard, bool, error) {
	resp, err := c.doWithRetry(ctx, func() (*http.Request, error) {
		return http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	})
	if err != nil {
		return nil, false, fmt.Errorf("request Scryfall search: %w", err)
	}
	defer drainAndClose(resp)
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize+1))
	if err != nil {
		return nil, false, err
	}
	if len(body) > maxResponseSize {
		return nil, false, errors.New("Scryfall search response is too large")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("Scryfall search returned HTTP %d", resp.StatusCode)
	}
	var page struct {
		Data     []scryfallCard `json:"data"`
		HasMore  bool           `json:"has_more"`
		NextPage string         `json:"next_page"`
	}
	if err := json.Unmarshal(body, &page); err != nil {
		return nil, false, fmt.Errorf("decode Scryfall search: %w", err)
	}
	return page.Data, page.HasMore, nil
}

type Client struct {
	baseURL    string
	httpClient *http.Client
	ttl        time.Duration
	// disk is the persistent tier behind the in-memory cache (nil when disabled):
	// it makes restarts warm and lets lookups ride out upstream outages with
	// stale entries instead of blank payloads.
	disk *diskCache
	mu   sync.RWMutex
	// cache maps normalized lookup keys to cards; entries are also mirrored to
	// disk on write.
	cache map[string]cacheEntry
}

type cacheEntry struct {
	card      Card
	expiresAt time.Time
}

type collectionResponse struct {
	Data     []scryfallCard    `json:"data"`
	NotFound []json.RawMessage `json:"not_found"`
}

type scryfallCard struct {
	OracleID      string            `json:"oracle_id"`
	Name          string            `json:"name"`
	PrintedName   string            `json:"printed_name"`
	ManaCost      string            `json:"mana_cost"`
	TypeLine      string            `json:"type_line"`
	OracleText    string            `json:"oracle_text"`
	ColorIdentity []string          `json:"color_identity"`
	Keywords      []string          `json:"keywords"`
	Legalities    map[string]string `json:"legalities"`
	ImageURIs     map[string]string `json:"image_uris"`
	Layout        string            `json:"layout"`
	Cmc           float64           `json:"cmc"`
	ProducedMana  []string          `json:"produced_mana"`
	Power         string            `json:"power"`
	Rank          int               `json:"edhrec_rank"`
	GameChanger   bool              `json:"game_changer"`
	CardFaces     []struct {
		Name        string `json:"name"`
		PrintedName string `json:"printed_name"`
		ManaCost    string `json:"mana_cost"`
		TypeLine    string `json:"type_line"`
		OracleText  string `json:"oracle_text"`
		// PrintedTypeLine / PrintedText carry the localized type line and
		// rules text of a non-English face (Scryfall only populates them on
		// prints in other languages).
		PrintedTypeLine string            `json:"printed_type_line,omitempty"`
		PrintedText     string            `json:"printed_text,omitempty"`
		ImageURIs       map[string]string `json:"image_uris"`
		GameChanger     bool              `json:"game_changer"`
		Power           string            `json:"power"`
	} `json:"card_faces"`
	// Localized fields (Scryfall returns these only for non-English lang requests).
	ChineseName     string `json:"-"`
	ChineseTypeLine string `json:"-"`
	ChineseOracle   string `json:"-"`
	PrintedTypeLine string `json:"printed_type_line,omitempty"`
	PrintedText     string `json:"printed_text,omitempty"`
}

// Autocomplete returns a list of canonical card names whose beginnings match the
// given (possibly partial) name, backed by Scryfall's dedicated /cards/autocomplete
// endpoint. Unlike /cards/named or /cards/search this endpoint is cheap and made
// exactly for typeahead: it never resolves card payloads, so callers who need color
// identity or legality must follow up with a normal Lookup. The list is capped and
// lowercased only for deduping; the original names are returned for display.
func (c *Client) Autocomplete(ctx context.Context, query string) ([]string, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, errors.New("autocomplete query is empty")
	}
	values := url.Values{}
	values.Set("q", query)
	endpoint := c.baseURL + "/cards/autocomplete?" + values.Encode()
	resp, err := c.doWithRetry(ctx, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", userAgent)
		return req, nil
	})
	if err != nil {
		return nil, fmt.Errorf("request Scryfall autocomplete: %w", err)
	}
	defer drainAndClose(resp)
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxResponseSize {
		return nil, errors.New("Scryfall autocomplete response is too large")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Scryfall autocomplete returned HTTP %d", resp.StatusCode)
	}
	var payload struct {
		Data []string `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("decode Scryfall autocomplete: %w", err)
	}
	// Scryfall can emit duplicates across faces; keep the first occurrence only.
	seen := make(map[string]struct{}, len(payload.Data))
	result := payload.Data[:0]
	for _, name := range payload.Data {
		key := normalizeName(name)
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, name)
	}
	return result, nil
}

// New builds a Scryfall catalog client. diskDir enables the persistent tier
// (empty string disables it — unit tests, hermetic environments); when enabled,
// entries live under <diskDir>/cards as one JSON file per lookup key.
func New(baseURL string, httpClient *http.Client, ttl time.Duration, diskDir string) *Client {
	return &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		httpClient: httpClient,
		ttl:        ttl,
		disk:       newDiskCache(diskDir),
		cache:      make(map[string]cacheEntry),
	}
}

func (c *Client) Lookup(ctx context.Context, names []string) (map[string]Card, error) {
	result := make(map[string]Card, len(names))
	missing := make([]string, 0, len(names))
	stale := make(map[string]Card)
	now := time.Now()
	for _, name := range uniqueNames(names) {
		key := normalizeName(name)
		c.mu.RLock()
		entry, ok := c.cache[key]
		c.mu.RUnlock()
		if ok && now.Before(entry.expiresAt) {
			result[key] = entry.card
			continue
		}
		// Disk tier: a fresh file serves directly (and warms the memory cache);
		// an expired one is held back as outage insurance for this call.
		served := false
		for _, candidate := range diskKeys(key) {
			card, fetchedAt, found := c.disk.get(candidate)
			if !found {
				continue
			}
			if now.Before(fetchedAt.Add(c.ttl)) {
				result[key] = card
				c.mu.Lock()
				c.cache[key] = cacheEntry{card: card, expiresAt: fetchedAt.Add(c.ttl)}
				c.mu.Unlock()
				served = true
			} else if _, kept := stale[key]; !kept {
				stale[key] = card
			}
			break
		}
		if served {
			continue
		}
		missing = append(missing, name)
	}
	var fetchErr error
	for start := 0; start < len(missing); start += maxBatchSize {
		end := min(start+maxBatchSize, len(missing))
		cards, err := c.fetchBatch(ctx, missing[start:end])
		if err != nil {
			fetchErr = err
			break
		}
		for _, card := range cards {
			for _, key := range cardLookupKeys(card) {
				result[key] = card
				c.mu.Lock()
				c.cache[key] = cacheEntry{card: card, expiresAt: time.Now().Add(c.ttl)}
				c.mu.Unlock()
				c.disk.put(key, card, time.Now())
			}
		}
	}
	if fetchErr != nil {
		// Upstream unavailable: serve stale disk entries instead of failing the
		// whole lookup. The error only surfaces when at least one requested name
		// has no fallback at all, so callers keep their "incomplete data" path.
		for key, card := range stale {
			result[key] = card
		}
		for _, name := range uniqueNames(names) {
			if _, ok := result[normalizeName(name)]; !ok {
				return result, fetchErr
			}
		}
		return result, nil
	}
	for _, name := range uniqueNames(names) {
		key := normalizeName(name)
		if _, ok := result[key]; ok {
			continue
		}
		if normalized := normalizeSplitName(key); normalized != key {
			if card, ok := result[normalized]; ok {
				result[key] = card
				continue
			}
		}
		if index := strings.Index(key, " // "); index > 0 {
			front := key[:index]
			if card, ok := result[front]; ok {
				result[key] = card
			}
		}
	}
	return result, nil
}

// LookupChinese resolves the Simplified Chinese (zhs) printing of a card by its
// English name: printed_name / printed_type_line / printed_text are
// copied into the Chinese* fields (per face for multi-faced cards). It backs
// the on-demand hover translation, so cost matters: results are cached under
// their own "zh:" namespace in both the memory and disk tiers, a card with no
// zhs printing is negative-cached as a name-only Card (Scryfall answers such
// searches with HTTP 404), and only real upstream failures are returned as
// errors.
func (c *Client) LookupChinese(ctx context.Context, name string) (Card, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Card{}, errors.New("card name is empty")
	}
	key := "zh:" + normalizeName(name)
	now := time.Now()
	c.mu.RLock()
	entry, ok := c.cache[key]
	c.mu.RUnlock()
	if ok && now.Before(entry.expiresAt) {
		return entry.card, nil
	}
	for _, candidate := range diskKeys(key) {
		if card, fetchedAt, found := c.disk.get(candidate); found {
			if now.Before(fetchedAt.Add(c.ttl)) {
				c.mu.Lock()
				c.cache[key] = cacheEntry{card: card, expiresAt: fetchedAt.Add(c.ttl)}
				c.mu.Unlock()
				return card, nil
			}
			break
		}
	}
	values := url.Values{}
	values.Set("q", `!"`+name+`" lang:zhs`)
	values.Set("unique", "prints")
	resp, err := c.doWithRetry(ctx, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/cards/search?"+values.Encode(), nil)
		if err != nil {
			return nil, err
		}
		// Same headers as every other client request: Scryfall rejects
		// headerless requests (observed as HTTP 400).
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", userAgent)
		return req, nil
	})
	if err != nil {
		return Card{}, fmt.Errorf("request Scryfall zhs search: %w", err)
	}
	defer drainAndClose(resp)
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize+1))
	if err != nil {
		return Card{}, err
	}
	if len(body) > maxResponseSize {
		return Card{}, errors.New("Scryfall zhs search response is too large")
	}
	card := Card{Name: name}
	switch {
	case resp.StatusCode == http.StatusOK:
		var page struct {
			Data []scryfallCard `json:"data"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return Card{}, fmt.Errorf("decode Scryfall zhs search: %w", err)
		}
		// The query returns every zhs print; they differ in how much got
		// localized, so prefer the hit carrying the most translated fields.
		bestScore := -1
		for _, hit := range page.Data {
			score := 0
			for _, field := range []string{hit.PrintedName, hit.PrintedTypeLine, hit.PrintedText} {
				if field != "" {
					score++
				}
			}
			if score > bestScore {
				card = localizedFromPrint(hit)
				bestScore = score
			}
			if score == 3 {
				break
			}
		}
	case resp.StatusCode == http.StatusNotFound:
		// Zero results for this exact-name zhs query: no Simplified Chinese
		// printing exists. Keep the name-only card as a negative cache entry.
	default:
		return Card{}, fmt.Errorf("Scryfall zhs search returned HTTP %d", resp.StatusCode)
	}
	expiresAt := now.Add(c.ttl)
	c.mu.Lock()
	c.cache[key] = cacheEntry{card: card, expiresAt: expiresAt}
	c.mu.Unlock()
	c.disk.put(key, card, now)
	return card, nil
}

// localizedFromPrint copies only the localized (printed_*) fields off a zhs
// search hit; Name stays the English canonical name so callers can key by it.
func localizedFromPrint(raw scryfallCard) Card {
	card := Card{
		Name:              raw.Name,
		ChineseName:       raw.PrintedName,
		ChineseTypeLine:   raw.PrintedTypeLine,
		ChineseOracleText: raw.PrintedText,
	}
	for _, face := range raw.CardFaces {
		card.Faces = append(card.Faces, CardFace{
			Name: face.Name, PrintedName: face.PrintedName,
			ChineseTypeLine: face.PrintedTypeLine, ChineseOracleText: face.PrintedText,
		})
	}
	return card
}

func (c *Client) fetchBatch(ctx context.Context, names []string) ([]Card, error) {
	identifiers := make([]map[string]string, 0, len(names))
	resolved := make([]string, 0, len(names))
	for _, name := range names {
		query := scryfallQueryName(name)
		identifiers = append(identifiers, map[string]string{"name": query})
		resolved = append(resolved, query)
	}
	body, err := json.Marshal(map[string]any{"identifiers": identifiers})
	if err != nil {
		return nil, err
	}
	resp, err := c.doWithRetry(ctx, func() (*http.Request, error) {
		// The body reader is rebuilt per attempt: a retried POST cannot reuse a
		// reader the previous attempt already consumed.
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/cards/collection", bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", userAgent)
		return req, nil
	})
	if err != nil {
		return nil, fmt.Errorf("request Scryfall: %w", err)
	}
	defer drainAndClose(resp)
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxResponseSize {
		return nil, errors.New("Scryfall response is too large")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Scryfall returned HTTP %d", resp.StatusCode)
	}
	var payload collectionResponse
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, fmt.Errorf("decode Scryfall response: %w", err)
	}
	cards := make([]Card, 0, len(payload.Data))
	for _, raw := range payload.Data {
		cards = append(cards, normalizeCard(raw))
	}
	// The collection endpoint intermittently fails to resolve prolifically
	// reprinted cards by name (Sol Ring is the classic case) while /cards/named
	// succeeds for the same name. Without this fallback a not_found silently
	// dropped the card from the batch — a builder staples panel would render
	// everything except that one card. The cap bounds worst-case latency when a
	// batch legitimately contains many nonexistent names.
	if len(payload.NotFound) > 0 {
		missed := make([]string, 0, len(payload.NotFound))
		for _, raw := range payload.NotFound {
			var miss struct {
				Name string `json:"name"`
			}
			if json.Unmarshal(raw, &miss) == nil && miss.Name != "" {
				missed = append(missed, miss.Name)
			}
		}
		if len(missed) > notFoundFallbackLimit {
			missed = missed[:notFoundFallbackLimit]
		}
		for _, name := range missed {
			if card, err := c.fetchNamed(ctx, name); err == nil {
				cards = append(cards, card)
			}
		}
	}
	return cards, nil
}

// fetchNamed resolves one card by exact name via /cards/named — the single-card
// fallback for identifiers the collection endpoint reports as not_found.
func (c *Client) fetchNamed(ctx context.Context, name string) (Card, error) {
	resp, err := c.doWithRetry(ctx, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/cards/named", nil)
		if err != nil {
			return nil, err
		}
		query := req.URL.Query()
		query.Set("exact", name)
		req.URL.RawQuery = query.Encode()
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", userAgent)
		return req, nil
	})
	if err != nil {
		return Card{}, fmt.Errorf("request Scryfall: %w", err)
	}
	defer drainAndClose(resp)
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize+1))
	if err != nil {
		return Card{}, err
	}
	if len(data) > maxResponseSize {
		return Card{}, errors.New("Scryfall response is too large")
	}
	if resp.StatusCode != http.StatusOK {
		return Card{}, fmt.Errorf("Scryfall returned HTTP %d", resp.StatusCode)
	}
	var raw scryfallCard
	if err := json.Unmarshal(data, &raw); err != nil {
		return Card{}, fmt.Errorf("decode Scryfall response: %w", err)
	}
	return normalizeCard(raw), nil
}

// scryfallQueryName maps a deck card name onto the identifier Scryfall accepts.
// Full split names like "X // Y" are not recognized by /cards/collection and
// come back as not_found, so we query the front face instead.
func scryfallQueryName(name string) string {
	name = strings.TrimSpace(name)
	if index := strings.Index(name, " // "); index > 0 {
		return strings.TrimSpace(name[:index])
	}
	if index := strings.Index(strings.ToLower(name), " /// "); index > 0 {
		return strings.TrimSpace(name[:index])
	}
	return name
}

func normalizeCard(raw scryfallCard) Card {
	manaCost, typeLine, oracleText := raw.ManaCost, raw.TypeLine, raw.OracleText
	images := raw.ImageURIs
	faces := make([]CardFace, 0, len(raw.CardFaces))
	for _, face := range raw.CardFaces {
		faces = append(faces, CardFace{Name: face.Name, PrintedName: face.PrintedName, ManaCost: face.ManaCost, TypeLine: face.TypeLine, OracleText: face.OracleText, ImageNormal: face.ImageURIs["normal"], ImageSmall: face.ImageURIs["small"], GameChanger: face.GameChanger, Power: face.Power})
	}
	if len(raw.CardFaces) > 0 {
		face := raw.CardFaces[0]
		if manaCost == "" {
			manaCost = face.ManaCost
		}
		if typeLine == "" {
			typeLine = face.TypeLine
		}
		if oracleText == "" {
			oracleText = face.OracleText
		}
		if len(images) == 0 {
			if len(face.ImageURIs) > 0 {
				images = face.ImageURIs
				faces[0].ImageNormal = face.ImageURIs["normal"]
				faces[0].ImageSmall = face.ImageURIs["small"]
			}
		}
	}
	return Card{
		OracleID: raw.OracleID, Name: raw.Name, PrintedName: raw.PrintedName,
		ManaCost: manaCost, TypeLine: typeLine, OracleText: oracleText,
		ColorIdentity: raw.ColorIdentity, Keywords: raw.Keywords, Legalities: raw.Legalities,
		ImageNormal: images["normal"], ImageSmall: images["small"], Layout: raw.Layout, Faces: faces,
		Cmc: raw.Cmc, ProducedMana: raw.ProducedMana, Power: raw.Power, Rank: raw.Rank, GameChanger: raw.GameChanger,
		ChineseName: raw.ChineseName, ChineseTypeLine: raw.ChineseTypeLine, ChineseOracleText: raw.ChineseOracle,
	}
}

func cardLookupKeys(card Card) []string {
	seen := make(map[string]struct{})
	var keys []string
	add := func(name string) {
		key := normalizeName(name)
		if key == "" {
			return
		}
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	add(card.Name)
	for _, face := range card.Faces {
		add(face.Name)
		if before, _, ok := strings.Cut(face.Name, " // "); ok {
			add(before)
		}
	}
	if before, _, ok := strings.Cut(card.Name, " // "); ok {
		add(before)
	}
	return keys
}

func normalizeName(value string) string { return strings.ToLower(strings.TrimSpace(value)) }

func normalizeSplitName(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	for _, sep := range []string{" // ", "///", "/"} {
		if index := strings.Index(value, sep); index > 0 {
			return strings.TrimSpace(value[:index])
		}
	}
	return value
}

func uniqueNames(names []string) []string {
	seen := make(map[string]struct{}, len(names))
	result := make([]string, 0, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		key := normalizeName(name)
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, name)
	}
	return result
}
