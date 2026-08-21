package edhrec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const maxResponseSize = 2 << 20

type Recommendation struct {
	Name                   string
	Synergy, InclusionRate float64
	SourceURL              string
}
type Group struct {
	Header string
	Tag    string
	Cards  []Recommendation
}
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// CommanderRanking is one commander entry from EDHREC's ranked popularity list,
// carrying just enough to bias a random draw toward less-popular commanders.
type CommanderRanking struct {
	Name      string
	DeckCount int
	IsPartner bool
}

type page struct {
	TagCounts []struct {
		Slug, Value string
		Count       int
	} `json:"tag_counts"`
	Container struct {
		JSONDict struct {
			CardLists []struct {
				Header    string `json:"header"`
				Tag       string `json:"tag"`
				CardViews []struct {
					Name, URL      string
					Synergy        float64 `json:"synergy"`
					NumDecks       int     `json:"num_decks"`
					PotentialDecks int     `json:"potential_decks"`
					IsPartner      bool    `json:"is_partner"`
				} `json:"cardviews"`
			} `json:"cardlists"`
		} `json:"json_dict"`
	} `json:"container"`
}

func New(baseURL string, httpClient *http.Client) *Client {
	return &Client{strings.TrimRight(baseURL, "/"), httpClient}
}

func (c *Client) Recommend(ctx context.Context, commanderSlug string, perGroupLimit int) ([]Group, []string, error) {
	if perGroupLimit < 1 {
		perGroupLimit = 20
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/pages/commanders/"+commanderSlug+".json", nil)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Referer", "https://edhrec.com/")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("request EDHREC: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize+1))
	if err != nil {
		return nil, nil, err
	}
	if resp.StatusCode != http.StatusOK {
		// Log response body for debugging
		bodyPreview := string(data)
		if len(bodyPreview) > 200 {
			bodyPreview = bodyPreview[:200]
		}
		return nil, nil, fmt.Errorf("EDHREC returned HTTP %d, body preview: %s", resp.StatusCode, bodyPreview)
	}
	var payload page
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, nil, err
	}
	keywords := make([]string, 0, min(6, len(payload.TagCounts)))
	for i, tag := range payload.TagCounts {
		if i >= 6 {
			break
		}
		keywords = append(keywords, tag.Value)
	}
	groups := make([]Group, 0, len(payload.Container.JSONDict.CardLists))
	for _, list := range payload.Container.JSONDict.CardLists {
		group := Group{Header: list.Header, Tag: list.Tag}
		for index, item := range list.CardViews {
			if index >= perGroupLimit {
				break
			}
			rate := 0.0
			if item.PotentialDecks > 0 {
				rate = float64(item.NumDecks) / float64(item.PotentialDecks)
			}
			group.Cards = append(group.Cards, Recommendation{Name: item.Name, Synergy: item.Synergy, InclusionRate: rate, SourceURL: "https://edhrec.com" + item.URL})
		}
		if len(group.Cards) > 0 {
			groups = append(groups, group)
		}
	}
	return groups, keywords, nil
}

// CommanderRankings fetches EDHREC's ranked commander list (last two years) and
// flattens it into name + deck-count pairs. The deck count lets a random draw bias
// toward less-popular commanders; is_partner marks cards that may pair with a second
// commander. The list is a single read of json.edhrec.com; it is not used for
// recommendations or scoring elsewhere.
func (c *Client) CommanderRankings(ctx context.Context) ([]CommanderRanking, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/pages/commanders/year.json", nil)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Referer", "https://edhrec.com/")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request EDHREC commander rankings: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize+1))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("EDHREC commander rankings returned HTTP %d", resp.StatusCode)
	}
	var payload page
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, err
	}
	rankings := make([]CommanderRanking, 0)
	for _, list := range payload.Container.JSONDict.CardLists {
		for _, item := range list.CardViews {
			name := strings.TrimSpace(item.Name)
			if name == "" {
				continue
			}
			rankings = append(rankings, CommanderRanking{
				Name:      name,
				DeckCount: item.NumDecks,
				IsPartner: item.IsPartner,
			})
		}
	}
	if len(rankings) == 0 {
		return nil, errors.New("EDHREC commander rankings list is empty")
	}
	return rankings, nil
}
