package manabase

import (
	"math"
	"regexp"
	"sort"
	"strings"

	"powerlevel/internal/providers/cardcatalog"
)

// CardFact is the minimal per-card data the classifier needs, shaped after
// Scryfall's card fields. Ported from DeckFlow.Core.Manabase.CardFact, trimmed to
// the fields the stage-1 classifier consumes.
type CardFact struct {
	Name string

	// Quantity is copies of this card in the deck.
	Quantity int

	// ManaCost is the Scryfall mana cost of the front face (e.g. "{2}{U}{U}"); empty
	// for lands.
	ManaCost string

	// ManaValue is the Scryfall mana value (cmc) of the front face.
	ManaValue float64

	// TypeLine is the Scryfall type line (e.g. "Legendary Creature — Elf Druid").
	TypeLine string

	// OracleText is the Scryfall oracle text (joined across faces).
	OracleText string

	// ProducedMana is Scryfall produced_mana letters (e.g. ["U","R","G"]).
	ProducedMana []string

	// HasLandFace is true when any face of the card is a land.
	HasLandFace bool

	// IsCommander is true when in the command zone rather than the library.
	IsCommander bool
}

var (
	reminderTextRe    = regexp.MustCompile(`\([^)]*\)`)
	basicFetchRe      = regexp.MustCompile(`(?i)search your library for`)
	basicLandRe       = regexp.MustCompile(`(?i)basic land`)
	tapClauseAddRe    = regexp.MustCompile(`(?i)\{t\}, tap[^.\n"\r]*:\s*add`)
	quotedSpanRe      = regexp.MustCompile(`"([^"]*)"`)
	appRestSelfPronRe = regexp.MustCompile(`(?i)\b(it|this creature|this permanent)\b.*\b(h|gains|has)\b`)
	payLifeRe         = regexp.MustCompile(`(?i)you may pay \d+ life`)
	scryRe            = regexp.MustCompile(`(?i)\bscry\s+([1-9]\d*)\b`)
)

// basicLandColors maps each basic land type to the color it taps for.
var basicLandColors = []struct {
	Type  string
	Color ManaColor
}{
	{"Plains", ColorWhite},
	{"Island", ColorBlue},
	{"Swamp", ColorBlack},
	{"Mountain", ColorRed},
	{"Forest", ColorGreen},
}

// classify maps a deck's worth of Scryfall card payloads into a ManabaseDeck ready
// for the analyzer. This is a simplified port of DeckFlow's ManabaseClassifier that
// covers the stage-1 surface: front-face lands, basic fetches, mana rocks/dorks at
// their Karsten weights, tapped/pay-life detection, and MV/pip parsing.
//
// Cards are paired with their quantity and command-zone status.
func classify(entries []ClassifyEntry) ManabaseDeck {
	facts := make([]CardFact, 0, len(entries))
	for _, e := range entries {
		facts = append(facts, toCardFact(e))
	}

	deckColorCount := countDeckColors(facts)
	fetchTypeColors, fetchBasicColors := buildFetchableColors(facts)

	var deck ManabaseDeck
	deck.IsSingleton = true

	var mvSum float64
	var mvSumWithLands float64
	var nonlandCount int
	var totalCount int
	var nonlandMVs []float64
	var allMVs []float64

	for _, card := range facts {
		deck.TotalCards += card.Quantity
		if card.IsCommander {
			deck.CommanderCount += card.Quantity
		}

		if isLandType(card.TypeLine) {
			addLandCopies(&deck, card, deckColorCount, fetchTypeColors, fetchBasicColors)
			// Lands count as 0 toward the all-cards stats.
			for i := 0; i < card.Quantity; i++ {
				allMVs = append(allMVs, 0)
			}
			totalCount += card.Quantity
			continue
		}

		// Spell front: contributes to the curve.
		if !card.IsCommander {
			mvSum += card.ManaValue * float64(card.Quantity)
			nonlandCount += card.Quantity
		}

		cost := parseManaCost(card.ManaCost)
		addSpellRequirement(&deck, card, cost)

		// Ramp/draw spells of MV <= 2 earn the land-target credit.
		if card.ManaValue <= 2 && isRampOrDraw(card) {
			deck.RampAndDrawUnderThree += card.Quantity
		}

		// 0-cost artifact fast mana (Lotus Petal, Mana Crypt) earns a land-target credit.
		if !card.HasLandFace && card.ManaValue == 0 && isType(card.TypeLine, "Artifact") && producesMana(card) {
			deck.FastMana += card.Quantity
		}

		addPartialSources(&deck, card)

		// Non-land cards feed both stats series: the curve series (non-land only) and
		// the all-cards series (lands weighted as 0). Commanders stay out of both —
		// they never enter the library and skew the curve.
		if !card.IsCommander {
			mvSumWithLands += card.ManaValue * float64(card.Quantity)
			for i := 0; i < card.Quantity; i++ {
				nonlandMVs = append(nonlandMVs, card.ManaValue)
				allMVs = append(allMVs, card.ManaValue)
			}
			totalCount += card.Quantity
		}
	}

	if nonlandCount > 0 {
		deck.AverageManaValue = round2(mvSum / float64(nonlandCount))
	}
	if totalCount > 0 {
		deck.AverageManaValueNoLands = round2(mvSumWithLands / float64(totalCount))
	}
	if len(nonlandMVs) > 0 {
		deck.MedianManaValue = round2(median(nonlandMVs))
	}
	if len(allMVs) > 0 {
		deck.MedianManaValueNoLands = round2(median(allMVs))
	}
	deck.TotalManaValue = int(mvSumWithLands)
	return deck
}

// median returns the middle value of an ascending-sorted copy of the slice. Even
// counts average the two middle values, matching the standard definition.
func median(values []float64) float64 {
	sorted := make([]float64, len(values))
	copy(sorted, values)
	sort.Float64s(sorted)
	n := len(sorted)
	if n%2 == 1 {
		return sorted[n/2]
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2
}

// ClassifyEntry pairs a resolved Scryfall card with its quantity and command-zone
// status. It is the Go mirror of DeckFlow.Core.Manabase.DeckCardEntry.
type ClassifyEntry struct {
	Card        cardcatalog.Card
	Quantity    int
	IsCommander bool
}

func toCardFact(e ClassifyEntry) CardFact {
	card := e.Card
	front := card.Faces
	var frontFace cardcatalog.CardFace
	hasFaces := len(front) > 0
	if hasFaces {
		frontFace = front[0]
	}

	typeLine := card.TypeLine
	if hasFaces && frontFace.TypeLine != "" {
		typeLine = frontFace.TypeLine
	}

	manaCost := card.ManaCost
	if hasFaces && frontFace.ManaCost != "" {
		manaCost = frontFace.ManaCost
	}

	// Prefer the front face's printed cost for the castable side; derive mana value
	// by parsing it so multi-faced cards use the front face rather than Scryfall's
	// combined cmc. Single-faced cards keep Scryfall's authoritative cmc.
	manaValue := card.ManaValue()
	if hasFaces {
		manaValue = float64(parseManaCost(manaCost).ManaValue)
	}

	fact := CardFact{
		Name:         card.Name,
		Quantity:     e.Quantity,
		ManaCost:     manaCost,
		ManaValue:    manaValue,
		TypeLine:     typeLine,
		OracleText:   joinOracleText(card),
		ProducedMana: card.Produced(),
		HasLandFace:  hasLandFace(card),
		IsCommander:  e.IsCommander,
	}
	return fact
}

func joinOracleText(card cardcatalog.Card) string {
	if len(card.Faces) > 0 {
		parts := make([]string, 0, len(card.Faces))
		for _, f := range card.Faces {
			if strings.TrimSpace(f.OracleText) != "" {
				parts = append(parts, f.OracleText)
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, "\n")
		}
	}
	return card.OracleText
}

func hasLandFace(card cardcatalog.Card) bool {
	if len(card.Faces) > 0 {
		frontIsLand := containsLand(card.Faces[0].TypeLine)
		mdfcBackIsLand := len(card.Faces) > 1 &&
			strings.EqualFold(card.Layout, "modal_dfc") &&
			containsLand(card.Faces[1].TypeLine)
		return frontIsLand || mdfcBackIsLand
	}
	return containsLand(card.TypeLine)
}

func containsLand(typeLine string) bool {
	return strings.Contains(strings.ToLower(typeLine), "land")
}

// --- classifier helpers -----------------------------------------------------

func countDeckColors(cards []CardFact) int {
	colors := map[ManaColor]struct{}{}
	for _, card := range cards {
		for color := range parseManaCost(card.ManaCost).Pips {
			if color != ColorColorless {
				colors[color] = struct{}{}
			}
		}
	}
	return len(colors)
}

func isLandType(typeLine string) bool {
	front := strings.Split(typeLine, "//")[0]
	return containsLand(front)
}

func isType(typeLine, typ string) bool {
	return strings.Contains(strings.ToLower(typeLine), strings.ToLower(typ))
}

func producesMana(card CardFact) bool {
	return strings.Contains(strings.ToLower(card.OracleText), "add ") || len(card.ProducedMana) > 0
}

func isRampOrDraw(card CardFact) bool {
	text := strings.ToLower(card.OracleText)
	ramp := (strings.Contains(text, "search your library for") && strings.Contains(text, "land")) ||
		strings.Contains(text, "add ") ||
		strings.Contains(text, "create a treasure")
	draw := strings.Contains(text, "draw a card") || strings.Contains(text, "draw two cards")
	return ramp || draw
}

func isRockOrDork(card CardFact) bool {
	if card.HasLandFace || len(card.ProducedMana) == 0 || !hasRepeatableManaAbility(card) {
		return false
	}
	return isType(card.TypeLine, "Creature") || isType(card.TypeLine, "Artifact")
}

// hasRepeatableManaAbility reports whether the card has a repeatable, non-sacrifice
// activated mana ability on its front face. Simplified from DeckFlow: it strips
// reminder text, then looks for a "{T}...: Add" clause that does not sacrifice,
// treating self-granted quoted abilities as the card's own.
func hasRepeatableManaAbility(card CardFact) bool {
	text := card.OracleText
	if strings.TrimSpace(text) == "" {
		return false
	}
	text = reminderTextRe.ReplaceAllString(text, "")
	for _, line := range strings.Split(text, "\n") {
		// Quoted grants that include the card itself count as its own ability;
		// other-grants are skipped.
		for _, quote := range quotedSpanRe.FindAllStringSubmatch(line, -1) {
			prefix := line[:strings.Index(line, quote[0])]
			if grantIncludesSelf(card, prefix) && lineHasActivatedAdd(quote[1]) {
				return true
			}
		}
		if lineHasActivatedAdd(quotedSpanRe.ReplaceAllString(line, "")) {
			return true
		}
	}
	return false
}

func lineHasActivatedAdd(line string) bool {
	colon := strings.Index(line, ":")
	if colon < 0 {
		return false
	}
	costPart := line[:colon]
	if strings.Contains(strings.ToLower(costPart), "sacrifice") {
		return false
	}
	effect := strings.TrimLeft(line[colon+1:], " ")
	return strings.HasPrefix(strings.ToLower(effect), "add ")
}

func grantIncludesSelf(card CardFact, prefix string) bool {
	prefix = strings.TrimRight(prefix, " ")
	if appRestSelfPronRe.MatchString(prefix) {
		return true
	}
	words := strings.Fields(prefix)
	for _, word := range words {
		word = strings.ToLower(word)
		if word == "" || isStopWord(word) {
			continue
		}
		singular := word
		if len(word) > 1 && strings.HasSuffix(word, "s") {
			singular = word[:len(word)-1]
		}
		if isType(card.TypeLine, singular) {
			return true
		}
	}
	return false
}

var grantStopWords = map[string]bool{
	"have": true, "has": true, "gain": true, "gains": true, "that": true,
	"this": true, "creature": true, "creatures": true, "each": true,
	"all": true, "you": true, "control": true, "your": true,
}

func isStopWord(word string) bool {
	return grantStopWords[word]
}

// addLandCopies classifies a front-face land (and its copies) into mana sources.
func addLandCopies(deck *ManabaseDeck, card CardFact, deckColorCount int, fetchTypeColors map[string][]ManaColor, fetchBasicColors []ManaColor) {
	produces := mapColors(card.ProducedMana)
	if len(produces) == 0 {
		// Fetchlands report empty produced_mana; derive colors from the basic land
		// types named in the fetch's oracle text.
		produces = fetchLandColors(card, fetchTypeColors, fetchBasicColors)
	}

	weight := 1.0
	if isBasicFetch(card) && deckColorCount >= 3 {
		weight = 0.67
	}

	untapped := !textEntersTapped(card.OracleText) || textPayLifeUntapped(card.OracleText)

	for i := 0; i < card.Quantity; i++ {
		deck.Sources = append(deck.Sources, ManaSource{
			Name:           card.Name,
			Produces:       produces,
			Weight:         weight,
			IsLand:         true,
			EntersUntapped: untapped,
			ManaAmount:     1,
			IsCommander:    card.IsCommander,
		})
	}
}

func addPartialSources(deck *ManabaseDeck, card CardFact) {
	if !isRockOrDork(card) {
		return
	}
	produces := mapColors(card.ProducedMana)
	if len(produces) == 0 {
		return
	}
	weight := 0.75
	if isType(card.TypeLine, "Creature") {
		weight = 0.5
	}
	for i := 0; i < card.Quantity; i++ {
		deck.Sources = append(deck.Sources, ManaSource{
			Name:       card.Name,
			Produces:   produces,
			Weight:     weight,
			IsLand:     false,
			ManaAmount: 1,
		})
	}
}

func addSpellRequirement(deck *ManabaseDeck, card CardFact, cost ParsedManaCost) {
	// X/Y/Z spells: printed mana value is not the real cast turn; skip them.
	if cost.HasVariableCost {
		return
	}
	mv := int(card.ManaValue)
	if mv < 0 {
		mv = 0
	}
	deck.Spells = append(deck.Spells, SpellRequirement{
		Name:              card.Name,
		ManaValue:         mv,
		Pips:              cost.Pips,
		IsGold:            distinctColors(cost.Pips) >= 2,
		IsManaSource:      isRockOrDork(card),
		IsPermanent:       isPermanent(card),
		IsCommander:       card.IsCommander,
		Quantity:          card.Quantity,
		TrueColorlessPips: cost.TrueColorlessPips,
		SnowPips:          cost.SnowPips,
	})
}

// isPermanent reports whether a card stays on the battlefield once cast: creature,
// artifact, enchantment, planeswalker, battle (and any type line containing those).
// Instants and sorceries are the non-permanent types. Lands never reach here — the
// classifier routes land type lines to sources before spells are built.
func isPermanent(card CardFact) bool {
	line := strings.ToLower(card.TypeLine)
	if strings.Contains(line, "instant") || strings.Contains(line, "sorcery") {
		return false
	}
	return isType(card.TypeLine, "Creature") ||
		isType(card.TypeLine, "Artifact") ||
		isType(card.TypeLine, "Enchantment") ||
		isType(card.TypeLine, "Planeswalker") ||
		isType(card.TypeLine, "Battle")
}

func distinctColors(pips map[ManaColor]int) int {
	count := 0
	for color, n := range pips {
		if n > 0 && color != ColorColorless {
			count++
		}
	}
	return count
}

// buildFetchableColors maps each basic land type to the colors every non-fetch land
// bearing that type can produce (so a typed fetch resolves to reachable colors), plus
// the set of basic-land colors the deck actually runs.
func buildFetchableColors(cards []CardFact) (map[string][]ManaColor, []ManaColor) {
	typeColors := make(map[string][]ManaColor)
	basicColors := map[ManaColor]struct{}{}

	for _, card := range cards {
		if !isLandType(card.TypeLine) {
			continue
		}
		colors := mapColors(card.ProducedMana)
		if len(colors) == 0 {
			continue // fetch or colorless utility land seeds no color
		}
		front := strings.Split(card.TypeLine, "//")[0]
		isBasic := strings.Contains(strings.ToLower(front), "basic")
		for _, bl := range basicLandColors {
			if !strings.Contains(strings.ToLower(front), strings.ToLower(bl.Type)) {
				continue
			}
			typeColors[bl.Type] = appendUnique(typeColors[bl.Type], colors...)
			if isBasic {
				basicColors[bl.Color] = struct{}{}
			}
		}
	}

	basicList := make([]ManaColor, 0, len(basicColors))
	for c := range basicColors {
		basicList = append(basicList, c)
	}
	return typeColors, basicList
}

func fetchLandColors(card CardFact, typeColors map[string][]ManaColor, basicColors []ManaColor) []ManaColor {
	text := strings.ToLower(card.OracleText)
	if !strings.Contains(text, "search your library") {
		return nil
	}
	var colors []ManaColor
	namedAny := false
	for _, bl := range basicLandColors {
		if !strings.Contains(text, strings.ToLower(bl.Type)) {
			continue
		}
		namedAny = true
		colors = appendUnique(colors, typeColors[bl.Type]...)
	}
	if !namedAny && strings.Contains(text, "basic land") {
		return basicColors
	}
	return colors
}

func appendUnique(dst []ManaColor, src ...ManaColor) []ManaColor {
	for _, c := range src {
		if !containsColor(dst, c) {
			dst = append(dst, c)
		}
	}
	return dst
}

func isBasicFetch(card CardFact) bool {
	text := strings.ToLower(card.OracleText)
	return strings.Contains(text, "search your library for a") && strings.Contains(text, "basic land")
}

func textEntersTapped(text string) bool {
	lower := strings.ToLower(text)
	return strings.Contains(lower, "enters tapped") || strings.Contains(lower, "enters the battlefield tapped")
}

func textPayLifeUntapped(text string) bool {
	clean := reminderTextRe.ReplaceAllString(text, "")
	return payLifeRe.MatchString(clean) && textEntersTapped(clean)
}

func round2(x float64) float64 {
	return math.Round(x*100) / 100
}
