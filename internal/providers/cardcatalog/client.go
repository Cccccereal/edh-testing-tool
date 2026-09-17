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
	maxBatchSize    = 75
	maxResponseSize = 8 << 20
	userAgent       = "PowerLevelAggregator/0.2"
)

type CardFace struct {
	Name        string `json:"name"`
	PrintedName string `json:"printed_name,omitempty"`
	ManaCost    string `json:"mana_cost,omitempty"`
	TypeLine    string `json:"type_line,omitempty"`
	OracleText  string `json:"oracle_text,omitempty"`
	ImageNormal string `json:"image_normal,omitempty"`
	ImageSmall  string `json:"image_small,omitempty"`
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
	GameChanger   bool              `json:"game_changer"`
	CardFaces     []struct {
		Name        string            `json:"name"`
		PrintedName string            `json:"printed_name"`
		ManaCost    string            `json:"mana_cost"`
		TypeLine    string            `json:"type_line"`
		OracleText  string            `json:"oracle_text"`
		ImageURIs   map[string]string `json:"image_uris"`
		GameChanger bool              `json:"game_changer"`
		Power       string            `json:"power"`
	} `json:"card_faces"`
	// Localized fields (Scryfall returns these only for non-English lang requests).
	ChineseName     string `json:"-"`
	ChineseTypeLine string `json:"-"`
	ChineseOracle   string `json:"-"`
	PrintedTypeLine string `json:"printed_type_line,omitempty"`
	PrintedOracle   string `json:"printed_oracle_text,omitempty"`
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
	return cards, nil
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
		Cmc: raw.Cmc, ProducedMana: raw.ProducedMana, Power: raw.Power, GameChanger: raw.GameChanger,
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
