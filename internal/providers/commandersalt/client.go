package commandersalt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"powerlevel/internal/deck"
)

const maxResponseSize = 4 << 20

type Client struct {
	baseURL    string
	httpClient *http.Client
}

type Result struct {
	Deck    deck.Deck
	Metrics map[string]any
}

// response mirrors the live api.commandersalt.com/decks shape as of 2026-09.
// Known drift since the original integration (both handled below): deckName is
// now null and the display name moved to title; the brackets object moved to
// details.brackets (still found by the full-payload walk); and the site grew
// combo/synergy/threat ratings next to the classic power/salt pair.
type response struct {
	ID               string                     `json:"id"`
	Name             string                     `json:"name"`
	Title            string                     `json:"title"`
	DeckName         string                     `json:"deckName"`
	Commanders       []string                   `json:"commanders"`
	Cards            map[string]responseCard    `json:"cards"`
	PowerLevel       json.RawMessage            `json:"powerLevel"`
	Ratings          json.RawMessage            `json:"ratings"`
	Pieces           map[string]json.RawMessage `json:"pieces"`
	PowerLevelRating float64                    `json:"powerLevelRating"`
	BracketRating    float64                    `json:"bracketRating"`
	SaltRating       float64                    `json:"saltRating"`
	ComboRating      float64                    `json:"comboRating"`
	SynergyRating    float64                    `json:"synergyRating"`
	ThreatRating     float64                    `json:"threatRating"`
	CardCount        int                        `json:"_cardCount"`
	Combos           json.RawMessage            `json:"combos"`
	Brackets         json.RawMessage            `json:"brackets"`
}

type responseCard struct {
	Name        string          `json:"name"`
	Count       json.RawMessage `json:"count"`
	Salt        string          `json:"salt"`
	IsCommander bool            `json:"isCommander"`
	IsFrontFace bool            `json:"isFrontFace"`
	Price       struct {
		USD string `json:"usd"`
	} `json:"price"`
	Types string `json:"types"`
}

func New(baseURL string, httpClient *http.Client) *Client {
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), httpClient: httpClient}
}

func (c *Client) Analyze(ctx context.Context, sourceURL, sourceID string) (Result, error) {
	endpoint, err := url.Parse(c.baseURL + "/decks")
	if err != nil {
		return Result{}, err
	}
	query := endpoint.Query()
	query.Set("url", sourceURL)
	endpoint.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), nil)
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json;charset=UTF-8")
	req.Header.Set("User-Agent", "PowerLevelAggregator/0.1")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("request CommanderSalt: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize+1))
	if err != nil {
		return Result{}, fmt.Errorf("read CommanderSalt response: %w", err)
	}
	if len(body) > maxResponseSize {
		return Result{}, errors.New("CommanderSalt response is too large")
	}
	if resp.StatusCode != http.StatusOK {
		return Result{}, fmt.Errorf("CommanderSalt returned HTTP %d", resp.StatusCode)
	}

	var payload response
	if err := json.Unmarshal(body, &payload); err != nil {
		return Result{}, fmt.Errorf("decode CommanderSalt response: %w", err)
	}
	var fullPayload map[string]json.RawMessage
	_ = json.Unmarshal(body, &fullPayload)

	// CommanderSalt returns each double-faced card's two faces as separate map
	// entries sharing a FrontFaceId, with only the front face flagged IsFrontFace.
	// The back face is deliberately skipped below (IsFrontFace gate at line ~110),
	// so nothing needs doing here — kept as a place to hang that note.

	resultDeck := deck.Deck{SourceURL: sourceURL, SourceID: sourceID, Name: payload.DeckName}
	if resultDeck.Name == "" {
		resultDeck.Name = payload.Title
	}
	if resultDeck.Name == "" {
		resultDeck.Name = strings.Join(payload.Commanders, " / ")
	}
	for _, item := range payload.Cards {
		quantity := parseQuantity(item.Count)
		if quantity <= 0 || !item.IsFrontFace {
			continue
		}
		card := deck.Card{
			Name:      item.Name,
			Quantity:  quantity,
			Commander: item.IsCommander,
			Salt:      parseFloat(item.Salt),
			Price:     parseFloat(item.Price.USD),
			TypeLine:  item.Types,
		}
		if card.Commander {
			resultDeck.Commanders = append(resultDeck.Commanders, card)
		} else {
			resultDeck.Mainboard = append(resultDeck.Mainboard, card)
		}
	}
	sort.Slice(resultDeck.Commanders, func(i, j int) bool { return resultDeck.Commanders[i].Name < resultDeck.Commanders[j].Name })
	sort.Slice(resultDeck.Mainboard, func(i, j int) bool { return resultDeck.Mainboard[i].Name < resultDeck.Mainboard[j].Name })
	if resultDeck.CardCount() == 0 {
		return Result{}, errors.New("CommanderSalt returned an empty deck")
	}

	rulesBracket, evaluatedBracket := parseBrackets(payload.Brackets)
	if rulesBracket == 0 && evaluatedBracket == 0 {
		rulesBracket, evaluatedBracket = parseBrackets(payload.PowerLevel)
	}
	if rulesBracket == 0 && evaluatedBracket == 0 {
		rulesBracket, evaluatedBracket = parseBrackets(payload.Ratings)
	}
	if rulesBracket == 0 && evaluatedBracket == 0 {
		for _, value := range fullPayload {
			rulesBracket, evaluatedBracket = parseBrackets(value)
			if rulesBracket > 0 || evaluatedBracket > 0 {
				break
			}
		}
	}
	if rulesBracket == 0 && evaluatedBracket == 0 {
		for _, piece := range payload.Pieces {
			rulesBracket, evaluatedBracket = parseBrackets(piece)
			if rulesBracket > 0 || evaluatedBracket > 0 {
				break
			}
		}
	}
	// Last resort for the display bracket: the flat top-level rating (float,
	// e.g. 3.51) that replaced the old nested object for clients that only read
	// scalars. The rules bracket keeps whatever the walk found — the flat field
	// does not distinguish WOTC tiers.
	if evaluatedBracket == 0 && payload.BracketRating > 0 {
		evaluatedBracket = int(payload.BracketRating + 0.5)
	}
	metrics := map[string]any{
		"power_level":       payload.PowerLevelRating,
		"rules_bracket":     rulesBracket,
		"evaluated_bracket": evaluatedBracket,
		"salt":              payload.SaltRating,
	}
	// New-generation ratings (2026-09 site update); only present when nonzero so
	// older cached payloads stay clean.
	if payload.ComboRating > 0 {
		metrics["combo"] = payload.ComboRating
	}
	if payload.SynergyRating > 0 {
		metrics["synergy"] = payload.SynergyRating
	}
	if payload.ThreatRating > 0 {
		metrics["threat"] = payload.ThreatRating
	}
	if suggestions, ok := parseSuggestions(fullPayload); ok {
		metrics["suggestions"] = suggestions
	}
	return Result{Deck: resultDeck, Metrics: metrics}, nil
}

func parseBrackets(raw json.RawMessage) (int, int) {
	var root map[string]json.RawMessage
	if json.Unmarshal(raw, &root) != nil {
		return 0, 0
	}
	csBracket := rawInt(root["csBracket"])
	var walk func(map[string]json.RawMessage) (int, int)
	walk = func(object map[string]json.RawMessage) (int, int) {
		rules := rawInt(object["wotcBracket"])
		evaluated := rawInt(object["displayBracket"])
		if rules > 0 || evaluated > 0 {
			return rules, evaluated
		}
		for _, value := range object {
			var child map[string]json.RawMessage
			if json.Unmarshal(value, &child) == nil {
				if rules, evaluated := walk(child); rules > 0 || evaluated > 0 {
					return rules, evaluated
				}
			}
		}
		return 0, 0
	}
	rules, evaluated := walk(root)
	if evaluated == 0 {
		evaluated = csBracket
	}
	return rules, evaluated
}

func rawInt(raw json.RawMessage) int {
	var value int
	_ = json.Unmarshal(raw, &value)
	return value
}

func parseQuantity(raw json.RawMessage) int {
	var number int
	if err := json.Unmarshal(raw, &number); err == nil {
		return number
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		number, _ = strconv.Atoi(text)
	}
	return number
}

func parseFloat(value string) float64 {
	number, _ := strconv.ParseFloat(value, 64)
	return number
}
