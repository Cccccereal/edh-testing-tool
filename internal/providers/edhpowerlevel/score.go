package edhpowerlevel

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"powerlevel/internal/deck"
	"powerlevel/internal/providers/spellbook"
)

// This file reimplements EDH Power Level's scoring without a headless browser.
// The site computes its score entirely client-side: it fetches per-card data from
// a Cloud Function (getcards) and runs a pure-JS formula. We replicate that formula
// here so the aggregator no longer depends on Chrome/Chromium.

const getcardsURL = "https://getcards-4zjvieuafa-uc.a.run.app"

// --- scoring factors (mirror of the site's `factors` object) ---
var (
	powerCurve = []float64{0, 250, 320, 350, 380, 420, 470, 560, 760, 890, 1000}
	priceCurve = []float64{0, 0.5, 1.5, 3.5, 6, 10, 15, 25, 40, 65, 100}
	popCurve   = []float64{0, 8500, 13600, 17100, 19800, 21900, 23700, 25300, 26200, 26700, 27000}

	bracketCurve = []float64{0, 4.7, 6.7, 7.7, 9.25, 10}

	landFactor     = 0.6
	reservedFactor = 0.2
	favorPrice     = 0.25
	cmcFloor       = 1.75
	cmcCeiling     = 6.0
	efficiencyLo   = 0.65
	efficiencyHi   = 1.10
)

// Ba — the per-card adjustment table the site hard-codes. It can override price
// (a multiplier), cmc (an absolute value), impact (a multiplier), commanderImpact
// (a multiplier applied only when the card is a commander), or producer (the set of
// mana colors a card can produce).
var adjustTable = []adjust{
	{name: "Cataclysm", impact: fp(1.7)},
	{name: "Jokulhaups", impact: fp(1.7)},
	{name: "Boom // Bust", impact: fp(1.7)},
	{name: "Armageddon", impact: fp(1.4)},
	{name: "Mystic Remora", price: fp(2)},
	{name: "Rest in Peace", price: fp(2.5)},
	{name: "The One Ring", price: fp(0.8)},
	{name: "Sylvan Library", price: fp(0.6)},
	{name: "Ragavan, Nimble Pilferer", price: fp(0.4)},
	{name: "Sol Ring", price: fp(8)},
	{name: "Fierce Guardianship", cmc: ip(0)},
	{name: "Deflecting Swat", cmc: ip(0)},
	{name: "Deadly Rollick", cmc: ip(0)},
	{name: "Flawless Maneuver", cmc: ip(0)},
	{name: "Obscuring Haze", cmc: ip(0)},
	{name: "Flare of Denial", cmc: ip(0)},
	{name: "Flare of Fortitude", cmc: ip(0)},
	{name: "Flare of Duplication", cmc: ip(0)},
	{name: "Flare of Malice", cmc: ip(0)},
	{name: "Flare of Cultivation", cmc: ip(0)},
	{name: "Endurance", cmc: ip(0)},
	{name: "Solitude", cmc: ip(0)},
	{name: "Grief", cmc: ip(0)},
	{name: "Subtlety", cmc: ip(0)},
	{name: "Fury", cmc: ip(0)},
	{name: "Force of Vigor", cmc: ip(0)},
	{name: "Force of Negation", cmc: ip(0)},
	{name: "Force of Despair", cmc: ip(0)},
	{name: "Force of Virtue", cmc: ip(0)},
	{name: "Force of Rage", cmc: ip(0)},
	{name: "Force of Will", cmc: ip(0)},
	{name: "Misdirection", cmc: ip(0)},
	{name: "Submerge", cmc: ip(0)},
	{name: "Snuff Out", cmc: ip(0)},
	{name: "Daze", cmc: ip(0)},
	{name: "Foil", cmc: ip(0)},
	{name: "Gush", cmc: ip(0)},
	{name: "Shriekmaw", cmc: ip(2)},
	{name: "Blasphemous Act", cmc: ip(3)},
	{name: "The Great Henge", cmc: ip(5)},
	{name: "Vandalblast", cmc: ip(3)},
	{name: "Cyclonic Rift", cmc: ip(5)},
	{name: "Everflowing Chalice", cmc: ip(2)},
	{name: "Dig Through Time", cmc: ip(4)},
	{name: "Temporal Trespass", cmc: ip(7)},
	{name: "Treasure Cruise", cmc: ip(3)},
	{name: "Emrakul, the Promised End", cmc: ip(7)},
	{name: "Vivi Ornitier", commanderImpact: fp(3)},
	{name: "Korvold, Fae-Cursed King", commanderImpact: fp(3)},
	{name: "Chulane, Teller of Tales", commanderImpact: fp(3)},
	{name: "Yuriko, the Tiger's Shadow", commanderImpact: fp(3)},
	{name: "Shirei, Shizo's Caretaker", commanderImpact: fp(2.5)},
	{name: "Orvar, the All-Form", commanderImpact: fp(3)},
	{name: "Magda, Brazen Outlaw", commanderImpact: fp(3.5)},
	{name: "Tergrid, God of Fright // Tergrid's Lantern", commanderImpact: fp(2.5)},
	{name: "Winota, Joiner of Forces", commanderImpact: fp(3)},
	{name: "Sisay, Weatherlight Captain", commanderImpact: fp(4)},
	{name: "Urza, Lord High Artificer", commanderImpact: fp(3)},
	{name: "Kinnan, Bonder Prodigy", commanderImpact: fp(4)},
	{name: "Yedora, Grave Gardener", commanderImpact: fp(2)},
	{name: "Kraum, Ludevic's Opus", commanderImpact: fp(2)},
	{name: "Thrasios, Triton Hero", commanderImpact: fp(3.5)},
	{name: "Tymna the Weaver", commanderImpact: fp(2)},
	{name: "Vial Smasher the Fierce", commanderImpact: fp(2)},
}

func fp(v float64) *float64 { return &v }
func ip(v int) *int         { return &v }

// fetch-land producers the site hard-codes for common fetch/filter lands so that a
// land contributing colors is accounted for even when its identity is colorless.
var producerOverrides = map[string][]string{
	"Terramorphic Expanse": {"W", "R", "G", "U", "B"},
	"Evolving Wilds":       {"W", "R", "G", "U", "B"},
	"Ash Barrens":          {"W", "R", "G", "U", "B", "C"},
	"Prismatic Vista":      {"W", "R", "G", "U", "B"},
	"Fabled Passage":       {"W", "R", "G", "U", "B"},
	"Krosan Verge":         {"W", "R", "G", "U", "B", "C"},
	"Myriad Landscape":     {"W", "R", "G", "U", "B", "C"},
}

var basicLands = map[string]bool{
	"Mountain": true, "Forest": true, "Island": true, "Swamp": true, "Plains": true, "Wastes": true,
	"Snow-Covered Mountain": true, "Snow-Covered Forest": true, "Snow-Covered Island": true,
	"Snow-Covered Swamp": true, "Snow-Covered Plains": true, "Snow-Covered Wastes": true,
}

// static rule lists for the Bracket rules computation (mirrors `cardLists`).
var (
	massLandDenialCards = []string{
		"Vorinclex, Voice of Hunger", "Hall of Gemstone", "Contamination", "Cataclysm",
		"Dimensional Breach", "Epicenter", "Global Ruin", "Hokori, Dust Drinker",
		"Razia's Purification", "Rising Waters", "Soulscour", "Sunder", "Apocalypse",
		"Bearer of the Heavens", "Conversion", "Glaciers", "Pox", "Death Cloud",
		"Tangle Wire", "Restore Balance", "Realm Razer", "Spreading Algae",
		"Numot, the Devastator", "Kudzu", "Demonic Hordes", "Urza's Sylex",
		"Infernal Darkness", "Trinisphere", "Worldfire", "Worldslayer",
		"Gilt-Leaf Archdruid", "Worldpurge", "Stasis",
	}
	mldWhitelist = []string{"Charitable Levy"}
	extraTurns   = []string{
		"Time Warp", "Temporal Manipulation", "Walk the Aeons", "Capture of Jingzhou",
		"Expropriate", "Time Stretch", "Nexus of Fate", "Timestream Navigator",
		"Sage of Hours", "Lighthouse Chronologist", "Time Sieve", "Magosi, the Waterveil",
	}
)

type adjust struct {
	name            string
	price           *float64
	cmc             *int
	impact          *float64
	commanderImpact *float64
	producer        []string
}

func adjustByName(name string) *adjust {
	for i := range adjustTable {
		if adjustTable[i].name == name {
			return &adjustTable[i]
		}
	}
	return nil
}

// --- getcards wire types ---

type getcardsResponse struct {
	Data    []rawCard `json:"data"`
	Details struct {
		Found     int `json:"found"`
		Requested int `json:"requested"`
	} `json:"details"`
}

type rawCard struct {
	Name         string          `json:"name"`
	FlavorName   json.RawMessage `json:"flavor_name"`
	Price        json.RawMessage `json:"price"`
	CMC          json.Number     `json:"cmc"`
	EDHRecRank   json.RawMessage `json:"edhrec_rank"`
	Colors       []string        `json:"colors"`
	ProducedMana []string        `json:"produced_mana"`
	ManaCost     string          `json:"mana_cost"`
	TypeLine     string          `json:"type_line"`
	Layout       string          `json:"layout"`
	Reserved     bool            `json:"reserved"`
	Legal        string          `json:"legal"`
	GameChanger  bool            `json:"gamechanger"`
	OracleText   []string        `json:"oracle_text"`
}

type card struct {
	name        string
	flavorNames []string
	price       float64
	cmc         int
	rank        float64
	colors      []string
	producer    []string
	manaCost    string
	typeLine    string
	layout      string
	reserved    bool
	gameChanger bool
	oracleText  []string
}

// getcardsClient fetches card data from EDH Power Level's Cloud Function. It is a
// plain HTTP client — no browser involved.
type getcardsClient struct {
	httpClient *http.Client
	mu         sync.Mutex
}

func newGetcardsClient(httpClient *http.Client) *getcardsClient {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &getcardsClient{httpClient: httpClient}
}

// fetch returns card data keyed by (lowercased) front-face name.
func (c *getcardsClient) fetch(ctx context.Context, cards []deck.Card) (map[string]*card, error) {
	if len(cards) == 0 {
		return map[string]*card{}, nil
	}
	names := make([]string, 0, len(cards))
	for _, card := range cards {
		for i := 0; i < card.Quantity; i++ {
			names = append(names, encodeCardName(frontFace(card.Name)))
		}
	}
	combo := buildCombo(cards)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, getcardsURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("card-list", strings.Join(names, "~"))
	req.Header.Set("combo-list", combo)
	req.Header.Set("User-Agent", "PowerLevelAggregator/0.5")

	c.mu.Lock()
	resp, err := c.httpClient.Do(req)
	c.mu.Unlock()
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("getcards returned HTTP %d", resp.StatusCode)
	}
	var payload getcardsResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("decode getcards: %w", err)
	}
	result := make(map[string]*card, len(payload.Data))
	for i := range payload.Data {
		raw := &payload.Data[i]
		c := &card{
			name:        raw.Name,
			flavorNames: flavorNames(raw.FlavorName),
			price:       numberOrRaw(raw.Price),
			cmc:         int(numberOrZero(raw.CMC)),
			rank:        numberOrRaw(raw.EDHRecRank),
			colors:      raw.Colors,
			producer:    raw.ProducedMana,
			manaCost:    raw.ManaCost,
			typeLine:    raw.TypeLine,
			layout:      raw.Layout,
			reserved:    raw.Reserved,
			gameChanger: raw.GameChanger,
			oracleText:  raw.OracleText,
		}
		result[strings.ToLower(strings.TrimSpace(raw.Name))] = c
		for _, flavor := range c.flavorNames {
			result[strings.ToLower(strings.TrimSpace(flavor))] = c
		}
	}
	return result, nil
}

// flavorNames decodes getcards' `flavor_name` field, which is an array of alternate
// (special-printing) names. Some exports — e.g. "Cyclonic Rift" printed as
// "Hope's Aero Magic" — only carry the flavor name, so we index the card by each so a
// flavor-named deck entry still resolves.
func flavorNames(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var names []string
	if err := json.Unmarshal(raw, &names); err == nil {
		return names
	}
	var single string
	if err := json.Unmarshal(raw, &single); err == nil {
		return []string{single}
	}
	return nil
}

func buildCombo(cards []deck.Card) string {
	main := make([]map[string]any, 0)
	commanders := make([]map[string]any, 0)
	for _, c := range cards {
		entry := map[string]any{"card": url.QueryEscape(frontFace(c.Name)), "quantity": c.Quantity}
		if c.Commander {
			commanders = append(commanders, entry)
		} else {
			main = append(main, entry)
		}
	}
	payload := map[string]any{"main": main, "commanders": commanders}
	out, _ := json.Marshal(payload)
	return string(out)
}

func numberOrZero(n json.Number) float64 {
	f, err := n.Float64()
	if err != nil {
		return 0
	}
	return f
}

// numberOrRaw parses an edhrec_rank that can be a number, "false", "null", or a
// JSON string. A missing/false rank (common for unranked cards) is treated as 0,
// which the site maps to maximum popularity via popCurve[len-1]-0.
func numberOrRaw(raw json.RawMessage) float64 {
	if len(raw) == 0 {
		return 0
	}
	if f, err := strconv.ParseFloat(string(raw), 64); err == nil {
		return f
	}
	// "false", "null", or a quoted number string.
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			return f
		}
	}
	return 0
}

func frontFace(name string) string {
	if idx := strings.Index(name, " // "); idx >= 0 {
		return name[:idx]
	}
	return name
}

// lookupCard resolves a deck card name against the getcards map, matching by exact
// name, front-face (for split cards), or a flavor/alternate-printing name so that
// exports using e.g. "Hope's Aero Magic" resolve to "Cyclonic Rift".
func lookupCard(cards map[string]*card, name string) *card {
	if c := cards[strings.ToLower(strings.TrimSpace(name))]; c != nil {
		return c
	}
	if c := cards[strings.ToLower(strings.TrimSpace(frontFace(name)))]; c != nil {
		return c
	}
	return nil
}

// encodeCardName percent-encodes a card name the way the site's frontend does before
// sending the `card-list` header. QueryEscape would turn spaces into "+"; the site
// uses %20 (RFC 3986 path encoding), so a card like "Yuna, Grand Summoner" must
// become "Yuna%2C%20Grand%20Summoner".
func encodeCardName(name string) string {
	escaped := url.QueryEscape(name)
	return strings.ReplaceAll(escaped, "+", "%20")
}

// --- the curve function (the site's `de`) ---

func curve(value float64, stops []float64, offset float64) float64 {
	if value <= stops[0] {
		return 0
	}
	if value > stops[len(stops)-1] {
		return float64(len(stops)-1) * offset
	}
	for i := 0; i < len(stops)-1; i++ {
		if value < stops[i+1] && value >= stops[i] {
			return float64(i)*offset + (value-stops[i])/(stops[i+1]-stops[i])
		}
	}
	return float64(len(stops)-1) * offset
}

// --- scoring ---

type scoredCard struct {
	card     *card
	quantity int
	isLand   bool
	impact   float64
	cmc      int
	// pips counts, per color, of the mana symbols in this card's mana cost (×quantity).
	pips map[string]int
}

type scoreResult struct {
	powerLevel float64
	metrics    map[string]any
}

// Score runs the full EDH Power Level formula against a deck and returns the metrics
// in the same shape the chromedp pipeline produced, so the rest of the app is unchanged.
// comboCategories, when non-nil, feeds the 2-card-combo bracket rule (early vs late).
func Score(ctx context.Context, target deck.Deck, httpClient *http.Client) (map[string]any, error) {
	return ScoreWithCombos(ctx, target, httpClient, nil, nil, nil)
}

// ScoreWithCombos is Score plus Commander Spellbook combo results, which the site's
// bracket rules use to detect early 2-card combos (combined mana cost ≤ 7).
// cardCMCByName (catalog CMCs, keyed by lowercased front face) sharpens the
// battlefield-cost sum; getcards CMCs fill any gaps. extraGameChangers carries
// Game Changer names from the app's own sources (Scryfall flag + snapshot) so the
// bracket counter does not depend on getcards' copy of the list alone.
func ScoreWithCombos(ctx context.Context, target deck.Deck, httpClient *http.Client, combos []spellbook.Combo, cardCMCByName map[string]int, extraGameChangers map[string]struct{}) (map[string]any, error) {
	all := append(append([]deck.Card{}, target.Commanders...), target.Mainboard...)
	cards, err := newGetcardsClient(httpClient).fetch(ctx, all)
	if err != nil {
		return nil, err
	}

	commanderNames := make(map[string]bool, len(target.Commanders))
	for _, c := range target.Commanders {
		commanderNames[frontFace(c.Name)] = true
	}

	scored := make([]*scoredCard, 0, len(all))
	for _, deckCard := range all {
		c := lookupCard(cards, deckCard.Name)
		if c == nil {
			// The site flags these as "not loaded" and refuses to score. We treat a
			// missing card as a hard error so callers surface it instead of a wrong total.
			return nil, fmt.Errorf("getcards did not return data for %q", deckCard.Name)
		}
		scored = append(scored, scoreOne(deckCard, c, commanderNames))
	}

	// totals
	var totalImpact, landImpact float64
	var lands, nonlands int
	var avgCostNumerator float64
	cmcImpact := make([]float64, 40) // non-land impact indexed by cmc
	cmcImpactColor := map[string][]float64{
		"R": make([]float64, 40), "W": make([]float64, 40), "G": make([]float64, 40),
		"U": make([]float64, 40), "B": make([]float64, 40), "C": make([]float64, 40),
	}

	for _, s := range scored {
		totalImpact += s.impact
		if s.isLand {
			lands += s.quantity
			landImpact += s.impact
		} else {
			nonlands += s.quantity
			avgCostNumerator += float64(s.cmc * s.quantity)
			idx := clamp(s.cmc, 0, len(cmcImpact)-1)
			cmcImpact[idx] += s.impact
			cols := s.card.colors
			if len(cols) == 0 {
				cmcImpactColor["C"][idx] += s.impact
			} else {
				per := s.impact / float64(len(cols))
				for _, col := range cols {
					if _, ok := cmcImpactColor[col]; ok {
						cmcImpactColor[col][idx] += per
					}
				}
			}
		}
	}

	// p = sum of non-land color impact (R+W+G+U+B+C)
	var p float64
	for _, col := range []string{"R", "W", "G", "U", "B", "C"} {
		for _, v := range cmcImpactColor[col] {
			p += v
		}
	}

	// Per-color producer counts (the site's `Ea`): a card "produces" a color if that
	// letter appears in its produced_mana; lands are treated as producing their colors
	// too. Commanders are excluded because they sit in the command zone, matching the
	// site's `k.length` subtraction.
	producerCount := map[string]int{"R": 0, "W": 0, "G": 0, "U": 0, "B": 0, "C": 0}
	commanderSet := make(map[string]bool, len(target.Commanders))
	for _, c := range target.Commanders {
		commanderSet[frontFace(c.Name)] = true
	}
	for _, s := range scored {
		if commanderSet[frontFace(s.card.name)] {
			continue
		}
		for _, col := range s.card.producer {
			if _, ok := producerCount[col]; ok {
				producerCount[col] += s.quantity
			}
		}
	}

	avgCost := 0.0
	if nonlands > 0 {
		avgCost = math.Round(avgCostNumerator/float64(nonlands)*100) / 100
	}

	// Ka = Tipping Point: smallest cmc where cumulative non-land impact > 65% of p.
	ka := 0
	cumulative := 0.0
	for i := 0; i < len(cmcImpact); i++ {
		cumulative += cmcImpact[i]
		if cumulative > p*0.65 {
			ka = i
			break
		}
	}

	g := (avgCost + float64(ka)) / 2
	ce := (cmcCeiling - g) / (cmcCeiling - cmcFloor)
	efficiency := efficiencyLo + (efficiencyHi-efficiencyLo)*ce
	score := totalImpact * efficiency
	powerLevel := curve(score, powerCurve, 1)

	// Bracket rules (minimum bracket) and evaluated bracket.
	rulesBracket, bdetails := computeBracket(scored, powerLevel, combos, effectiveCMCs(scored, cardCMCByName), extraGameChangers)

	metrics := map[string]any{
		"power_level": roundTo(powerLevel, 2),
		"efficiency":  roundTo(ce*10, 2),
		"score":       math.Round(score),
		"impact":      roundTo(totalImpact, 2),
		// Percentage (0-100), matching the site's display; the frontend appends "%".
		"average_playability": roundTo(averagePlayability(scored, producerCount, lands, nonlands, len(target.Commanders))*100, 1),
		"rules_bracket":       displayRulesBracket(rulesBracket),
		"evaluated_bracket":   evaluatedBracket(powerLevel, rulesBracket),
	}
	if bdetails != nil {
		metrics["bracket_details"] = bdetails
	}
	return metrics, nil
}

func scoreOne(deckCard deck.Card, c *card, commanderNames map[string]bool) *scoredCard {
	name := frontFace(c.name)
	price := c.price
	cmc := c.cmc
	impact := 1.0

	if adj := adjustByName(name); adj != nil {
		if adj.price != nil {
			price *= *adj.price
		}
		if adj.cmc != nil {
			cmc = *adj.cmc
		}
		if adj.impact != nil {
			impact *= *adj.impact
		}
		if adj.commanderImpact != nil && commanderNames[name] {
			impact *= *adj.commanderImpact
		}
	}
	if c.reserved {
		price *= reservedFactor
	}
	if pv, ok := producerOverrides[name]; ok {
		c.producer = pv
	}

	priceRating := curve(price, priceCurve, 1+favorPrice)
	popRating := curve(popCurve[len(popCurve)-1]-c.rank, popCurve, 1-favorPrice)

	baseImpact := (priceRating + popRating) * float64(deckCard.Quantity) * impact

	typeString := strings.TrimSpace(strings.Split(c.typeLine, " — ")[0])
	isLand := strings.Contains(typeString, "Land") || c.layout == "modal_dfc"

	s := &scoredCard{card: c, quantity: deckCard.Quantity, isLand: isLand, cmc: cmc, pips: countPips(c.manaCost, deckCard.Quantity)}
	if isLand {
		s.impact = baseImpact * landFactor
		s.cmc = 0
	} else {
		s.impact = baseImpact
	}
	if basicLands[name] {
		s.impact = 2 * float64(deckCard.Quantity)
	}
	return s
}

// countPips counts each colored pip symbol in a mana cost (e.g. "{1}{G}{W}{U}"
// yields G:1, W:1, U:1) multiplied by the card's quantity. It mirrors the site's
// `ha` regex: R, W, G, U, B are matched individually; C matches "{C}" or "{C/W}"
// style hybrid-cost colorless pips.
func countPips(manaCost string, quantity int) map[string]int {
	result := map[string]int{}
	for _, match := range colorPipRe.FindAllString(manaCost, -1) {
		letter := strings.Trim(match, "{}")
		result[letter] += quantity
	}
	for range colorlessPipRe.FindAllString(manaCost, -1) {
		result["C"] += quantity
	}
	return result
}

var (
	colorPipRe     = regexp.MustCompile(`\{W\}|\{U\}|\{B\}|\{R\}|\{G\}`)
	colorlessPipRe = regexp.MustCompile(`\{C(?:/[WUBRG])?\}`)
)

func averagePlayability(scored []*scoredCard, producerCount map[string]int, lands, nonlands, commanderCount int) float64 {
	// Playability mirrors the site's `oe` function: for each non-land card, the
	// probability it can be cast at (cmc + 7) draws is the product of
	// (1 - hypergeometricCDF(pips[color]-1, deckSize, producerCount[color], cmc+7))
	// over the colors it needs, times the probability of having drawn enough total
	// lands. It's informational only and does not feed the power score.
	deckSize := lands + nonlands - commanderCount
	colors := []string{"R", "W", "G", "U", "B"}
	total := 0.0
	count := 0
	for _, s := range scored {
		if s.isLand {
			continue
		}
		cmc := s.cmc
		draws := cmc + 7
		playability := 1.0
		for _, col := range colors {
			if s.pips[col] <= 0 {
				continue
			}
			// P(X <= pips-1) where X ~ Hypergeometric(N=deckSize, K=producers, n=draws).
			playability *= 1 - hypergeometricCDF(s.pips[col]-1, deckSize, producerCount[col], draws)
		}
		// probability of drawing enough lands for the generic {N} portion.
		if cmc > 0 {
			playability *= 1 - hypergeometricCDF(cmc-1, deckSize, lands, draws)
		}
		total += playability
		count++
	}
	if count == 0 {
		return 0
	}
	return total / float64(count)
}

// hypergeometricCDF returns P(X <= x) for X ~ Hypergeometric(N, K, n): a population
// of N with K "successes", drawing n without replacement. It is the exact CDF used
// by the site's stdlib-backed distribution, matching the pmf-recurrence from below.
func hypergeometricCDF(x, N, K, n int) float64 {
	if x < 0 || N <= 0 || n <= 0 || K <= 0 {
		return 0
	}
	lo := max(0, n+K-N)
	hi := min(n, K)
	if lo > hi {
		return 0
	}
	// pmf(k) = C(K,k) C(N-K,n-k) / C(N,n), computed in log space to avoid overflow.
	logChoose := func(m, k int) float64 {
		if k < 0 || k > m {
			return math.Inf(-1)
		}
		var result float64
		for i := 1; i <= k; i++ {
			result += math.Log(float64(m-k+i)) - math.Log(float64(i))
		}
		return result
	}
	logDenom := logChoose(N, n)
	cdf := 0.0
	for k := lo; k <= x && k <= hi; k++ {
		logPmf := logChoose(K, k) + logChoose(N-K, n-k) - logDenom
		cdf += math.Exp(logPmf)
	}
	if cdf > 1 {
		cdf = 1
	}
	return cdf
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// --- bracket rules ---

// Per-bracket allowances (index 0 = Bracket 1), mirroring the site's
// `bracketHelper` rule tables: when a rule's match count exceeds maxes[idx],
// the minimum internal bracket becomes idx+1.
var (
	turnsMaxes       = []int{0, 2, 3, 100, 100}
	denialMaxes      = []int{0, 0, 0, 100, 100}
	gameChangerMaxes = []int{0, 0, 3, 100, 100}
	earlyComboMaxes  = []int{0, 0, 0, 100, 100}
	lateComboMaxes   = []int{0, 0, 100, 100, 100}
)

// restrictedUnderBracket is the internal minimum bracket any named restricted
// card forces (the site's restricted.underBracket): a single classic extra-turn
// or mass-land-denial card pushes the deck to Bracket 4 or higher.
const restrictedUnderBracket = 3

// bracketDetails mirrors the chromedp pipeline's rule-bracket detail blob so the
// frontend keeps rendering bracket breakdowns identically.
type bracketDetails struct {
	RulesBracketReasons    []string `json:"rules_bracket_reasons"`
	EvaluatedBracketReason string   `json:"evaluated_bracket_reason"`
	GameChangers           int      `json:"game_changers"`
	EarlyTwoCardCombos     int      `json:"early_2_card_combos"`
	ExtraTurns             int      `json:"extra_turns"`
	MassLandDenial         int      `json:"mass_land_denial"`
	GameChangerNames       []string `json:"game_changer_names"`
	EarlyTwoCardComboNames []string `json:"early_2_card_combo_names"`
	ExtraTurnNames         []string `json:"extra_turn_names"`
	MassLandDenialNames    []string `json:"mass_land_denial_names"`

	lateComboNames []string
}

// computeBracket returns the internal minimum bracket `na` — the site's own
// variable: 0 when no rule is breached, otherwise the highest breached bracket
// index plus one. The user-facing rules bracket is displayRulesBracket(na);
// the evaluated bracket is max(power curve, na+1), exactly like the site's
// `xa = X > na+1 ? X : na+1`.
func computeBracket(scored []*scoredCard, powerLevel float64, combos []spellbook.Combo, cardCMCByName map[string]int, extraGameChangers map[string]struct{}) (int, *bracketDetails) {
	details := &bracketDetails{}

	// The site tracks regex matches and named restricted-list matches separately:
	// regex hits count against the per-bracket maxes, any named hit forces the
	// restricted minimum. Display lists merge both.
	var turnRegex, turnNamed, denialRegex, denialNamed []string
	for _, s := range scored {
		name := frontFace(s.card.name)
		for _, line := range s.card.oracleText {
			text := strings.ToLower(line)
			if extraTurnRe.MatchString(text) && !contains(turnRegex, name) {
				turnRegex = append(turnRegex, name)
			}
			if denialRe.MatchString(text) && !contains(denialRegex, name) && !contains(mldWhitelist, name) {
				denialRegex = append(denialRegex, name)
			}
		}
		if (s.card.gameChanger || gameChangerSetHas(extraGameChangers, name)) && !contains(details.GameChangerNames, name) {
			details.GameChangerNames = append(details.GameChangerNames, name)
		}
		if contains(extraTurns, name) && !contains(turnNamed, name) {
			turnNamed = append(turnNamed, name)
		}
		if contains(massLandDenialCards, name) && !contains(denialNamed, name) {
			denialNamed = append(denialNamed, name)
		}
	}
	details.ExtraTurnNames = mergeNames(turnRegex, turnNamed)
	details.MassLandDenialNames = mergeNames(denialRegex, denialNamed)
	details.ExtraTurns = len(details.ExtraTurnNames)
	details.MassLandDenial = len(details.MassLandDenialNames)
	details.GameChangers = len(details.GameChangerNames)

	// Early/late 2-card combo detection (mirrors the site's `Ke.forEach` block). A
	// combo is "early" when its total mana (mv + battlefield card cmcs + any addenda)
	// is ≤ 7; otherwise it's "late".
	for _, combo := range combos {
		if len(combo.Components) < 2 {
			continue
		}
		w := len(combo.Components)
		producesSomething := false
		for _, id := range combo.ProducesFeatureIDs {
			if !containsInt(gameDefiningProducers, id) {
				producesSomething = true
			}
		}
		requiresOK := true
		if len(combo.RequiresTemplates) == 0 {
			requiresOK = true
		} else {
			requiresOK = false
			for _, tid := range combo.RequiresTemplates {
				if !containsInt(acceptableRequirementTemplates, tid) {
					requiresOK = true
				}
			}
			// A combo is still viable if it has a small total card count (< 3); we don't
			// have per-require quantity here, so we treat any acceptable requirement as
			// keeping the combo eligible (the site only disqualifies on the "w < 3" edge).
		}
		ok := producesSomething && requiresOK && w < 3
		if !ok {
			continue
		}
		ya := 0
		for _, comp := range combo.Components {
			if strings.Contains(comp.Zone, "B") {
				if cmc, found := cardCMCByName[strings.ToLower(frontFace(comp.Name))]; found {
					ya += cmc
				}
			}
		}
		combined := combo.ManaValueNeeded + ya
		if combined > 7 {
			if !contains(details.lateComboNames, combo.Name) {
				details.lateComboNames = append(details.lateComboNames, combo.Name)
			}
		} else {
			if !contains(details.EarlyTwoCardComboNames, combo.Name) {
				details.EarlyTwoCardComboNames = append(details.EarlyTwoCardComboNames, combo.Name)
			}
		}
	}
	details.EarlyTwoCardCombos = len(details.EarlyTwoCardComboNames)

	// bump mirrors the site's per-rule minimum: the highest maxes index whose
	// count breaches, plus one; a named restricted card forces the minimum up.
	bump := func(count int, maxes []int, restricted bool) int {
		h := 0
		for idx := len(maxes) - 1; idx >= 0; idx-- {
			if count > maxes[idx] {
				h = idx + 1
				break
			}
		}
		if restricted && h < restrictedUnderBracket {
			h = restrictedUnderBracket
		}
		return h
	}
	na := maxOf(
		bump(len(turnRegex), turnsMaxes, len(turnNamed) > 0),
		bump(len(denialRegex), denialMaxes, len(denialNamed) > 0),
		bump(details.GameChangers, gameChangerMaxes, false),
		bump(details.EarlyTwoCardCombos, earlyComboMaxes, false),
		bump(len(details.lateComboNames), lateComboMaxes, false),
	)

	// build reasons
	if details.GameChangers > 3 {
		details.RulesBracketReasons = append(details.RulesBracketReasons,
			fmt.Sprintf("Game Changers: %d - Your deck contains %d card(s) from the gamechanger list.", details.GameChangers, details.GameChangers))
	}
	if details.EarlyTwoCardCombos > 0 {
		details.RulesBracketReasons = append(details.RulesBracketReasons,
			fmt.Sprintf("Early 2-Card Combos: %d - Your deck contains %d early 2-card combo(s).", details.EarlyTwoCardCombos, details.EarlyTwoCardCombos))
	}
	if details.ExtraTurns > 0 {
		details.RulesBracketReasons = append(details.RulesBracketReasons,
			fmt.Sprintf("Extra Turns: %d - Your deck contains cards that take extra turns.", details.ExtraTurns))
	}
	if details.MassLandDenial > 0 {
		details.RulesBracketReasons = append(details.RulesBracketReasons,
			fmt.Sprintf("Mass Land Denial: %d - Your deck contains mass land denial.", details.MassLandDenial))
	}

	details.EvaluatedBracketReason = fmt.Sprintf("Recommended Bracket: %d. Minimum Bracket: %d.", evaluatedBracket(powerLevel, na), displayRulesBracket(na))
	return na, details
}

// displayRulesBracket converts the internal minimum `na` into the 1-based bracket
// shown to users, matching the site's `na>0 ? na+1 : 1`.
func displayRulesBracket(na int) int {
	if na > 0 {
		return na + 1
	}
	return 1
}

// gameChangerSetHas reports whether name (front face, case-insensitive) is in
// the extra Game Changer set handed in by the caller.
func gameChangerSetHas(set map[string]struct{}, name string) bool {
	if len(set) == 0 {
		return false
	}
	_, ok := set[strings.ToLower(name)]
	return ok
}

// mergeNames concatenates two name lists, dropping duplicates (the regex lists
// and the named restricted lists overlap heavily).
func mergeNames(primary, secondary []string) []string {
	merged := append([]string{}, primary...)
	for _, name := range secondary {
		if !contains(merged, name) {
			merged = append(merged, name)
		}
	}
	return merged
}

func maxOf(values ...int) int {
	result := 0
	for _, v := range values {
		if v > result {
			result = v
		}
	}
	return result
}

// effectiveCMCs overlays the getcards CMCs beneath the catalog's: the bracket
// combo detector sums battlefield component costs, and a component the catalog
// lacks (Scryfall down, or the card missing from it) still has a getcards value.
func effectiveCMCs(scored []*scoredCard, catalogCMC map[string]int) map[string]int {
	merged := make(map[string]int, len(catalogCMC)+len(scored))
	for name, cmc := range catalogCMC {
		merged[name] = cmc
	}
	for _, s := range scored {
		key := strings.ToLower(frontFace(s.card.name))
		if _, ok := merged[key]; !ok {
			merged[key] = s.cmc
		}
	}
	return merged
}

// gameDefiningProducers and acceptableRequirementTemplates are the numeric feature /
// template IDs the site's `Ee` object hard-codes. The lists are opaque IDs from
// Commander Spellbook's DB; we reproduce the exact sets so early/late classification
// matches the reference implementation.
var (
	gameDefiningProducers          = []int{7, 4, 3, 6, 17, 77, 60, 79, 325, 110, 505, 1111, 227, 87, 547, 2742, 2617}
	acceptableRequirementTemplates = []int{28}
)

func containsInt(list []int, value int) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}

var (
	extraTurnRe = regexp.MustCompile(`(take an extra turn|target player takes an extra turn|target player takes \w* extra turns)`)
	denialRe    = regexp.MustCompile(`(^(noncreature|creature|red|white|blue|black|green) spells|^spells your opponents cast) cost \{\d+\} more to cast|each player sacrifices \w* (lands|land for each)|destroy all (lands|islands|mountains|forests|swamps|plains)|destroy all (\w*, )*and lands|untap (only|more than) \w* (land|permanent|nonbasic)|(islands|mountains|forests|swamps|plains|\w* lands) don't untap|nonbasic lands are (mountains|islands)`)
)

func evaluatedBracket(powerLevel float64, na int) int {
	// X = ceil(de(power, bracketCurve)); recommended = max(X, na+1) capped at 5.
	// The site computes the same thing (`bracketHelper(ie)` with ie = power
	// level): a clean weak deck can be recommended Bracket 1.
	x := 5
	if powerLevel > 0 {
		x = int(math.Ceil(curve(powerLevel, bracketCurve, 1)))
	}
	recommended := x
	if candidate := na + 1; candidate > recommended {
		recommended = candidate
	}
	if recommended > 5 {
		recommended = 5
	}
	if recommended < 1 {
		recommended = 1
	}
	return recommended
}

func contains(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func roundTo(f float64, places int) float64 {
	shift := math.Pow(10, float64(places))
	return math.Round(f*shift) / shift
}
