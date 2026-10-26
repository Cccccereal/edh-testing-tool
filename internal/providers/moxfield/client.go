package moxfield

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"powerlevel/internal/deck"
)

const maxResponseSize = 8 << 20

type Client struct {
	baseURL    string
	httpClient *http.Client
}

type Error struct {
	Code   string
	Status int
}

func (e *Error) Error() string {
	return fmt.Sprintf("Moxfield deck source failed: %s (HTTP %d)", e.Code, e.Status)
}

func New(baseURL string, httpClient *http.Client) *Client {
	return &Client{strings.TrimRight(baseURL, "/"), httpClient}
}

func (c *Client) Load(ctx context.Context, sourceURL, sourceID string) (deck.Deck, error) {
	endpoint := c.baseURL + "/v3/decks/all/" + sourceID
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return deck.Deck{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "PowerLevelAggregator/0.5")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return deck.Deck{}, fmt.Errorf("request Moxfield: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize+1))
	if err != nil {
		return deck.Deck{}, err
	}
	if len(data) > maxResponseSize {
		return deck.Deck{}, errors.New("Moxfield response is too large")
	}
	contentType := strings.ToLower(resp.Header.Get("Content-Type"))
	if strings.Contains(contentType, "text/html") || strings.Contains(strings.ToLower(string(data[:min(len(data), 512)])), "cloudflare") {
		return deck.Deck{}, &Error{Code: "UPSTREAM_CHALLENGE", Status: resp.StatusCode}
	}
	if resp.StatusCode == http.StatusNotFound {
		return deck.Deck{}, &Error{Code: "NOT_FOUND", Status: resp.StatusCode}
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return deck.Deck{}, &Error{Code: "PRIVATE_OR_FORBIDDEN", Status: resp.StatusCode}
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return deck.Deck{}, &Error{Code: "RATE_LIMITED", Status: resp.StatusCode}
	}
	if resp.StatusCode != http.StatusOK {
		return deck.Deck{}, &Error{Code: "BAD_STATUS", Status: resp.StatusCode}
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(data, &payload); err != nil {
		return deck.Deck{}, fmt.Errorf("decode Moxfield response: %w", err)
	}
	result := deck.Deck{SourceURL: sourceURL, SourceID: sourceID, Name: rawString(payload["name"])}
	zones := payload
	if boards, ok := payload["boards"]; ok {
		var boardMap map[string]json.RawMessage
		if json.Unmarshal(boards, &boardMap) == nil {
			zones = boardMap
		}
	}
	result.Commanders = parseZone(zoneCards(zones["commanders"]), true)
	result.Mainboard = parseZone(zoneCards(zones["mainboard"]), false)
	// Moxfield's boards are JSON objects keyed by card ID, so parseZone walks them
	// in Go's randomized map order — without sorting, the deck list and every
	// export derived from it (deck_cards, editor state) reshuffle per analysis.
	sort.Slice(result.Commanders, func(i, j int) bool { return result.Commanders[i].Name < result.Commanders[j].Name })
	sort.Slice(result.Mainboard, func(i, j int) bool { return result.Mainboard[i].Name < result.Mainboard[j].Name })
	if len(result.Commanders) == 0 {
		return deck.Deck{}, errors.New("Moxfield deck has no commander")
	}
	if result.CardCount() == 0 {
		return deck.Deck{}, errors.New("Moxfield returned an empty deck")
	}
	return result, nil
}

func zoneCards(raw json.RawMessage) json.RawMessage {
	var board map[string]json.RawMessage
	if json.Unmarshal(raw, &board) == nil {
		if cards, ok := board["cards"]; ok {
			return cards
		}
	}
	return raw
}

func parseZone(raw json.RawMessage, commander bool) []deck.Card {
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) == nil {
		result := make([]deck.Card, 0, len(object))
		for _, entry := range object {
			if card, ok := parseEntry(entry, commander); ok {
				result = append(result, card)
			}
		}
		return result
	}
	var array []json.RawMessage
	if json.Unmarshal(raw, &array) == nil {
		result := make([]deck.Card, 0, len(array))
		for _, entry := range array {
			if card, ok := parseEntry(entry, commander); ok {
				result = append(result, card)
			}
		}
		return result
	}
	return nil
}

func parseEntry(raw json.RawMessage, commander bool) (deck.Card, bool) {
	var entry map[string]json.RawMessage
	if json.Unmarshal(raw, &entry) != nil {
		return deck.Card{}, false
	}
	quantity := rawInt(entry["quantity"])
	if quantity < 1 {
		quantity = 1
	}
	name := rawString(entry["name"])
	if cardRaw, ok := entry["card"]; ok {
		var card map[string]json.RawMessage
		if json.Unmarshal(cardRaw, &card) == nil {
			name = rawString(card["name"])
		}
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return deck.Card{}, false
	}

	return deck.Card{Name: name, Quantity: quantity, Commander: commander}, true
}
func rawString(raw json.RawMessage) string {
	var value string
	_ = json.Unmarshal(raw, &value)
	return value
}
func rawInt(raw json.RawMessage) int {
	var value int
	if json.Unmarshal(raw, &value) == nil {
		return value
	}
	var text string
	_ = json.Unmarshal(raw, &text)
	fmt.Sscanf(text, "%d", &value)
	return value
}
