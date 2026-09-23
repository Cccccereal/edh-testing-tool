package service

import (
	"context"
	"errors"
	"sort"
	"strings"

	"powerlevel/internal/providers/cardcatalog"
)

// errUnknownStapleCategory is returned when a quick-add group id is not in the
// catalog of common-staple groups.
var errUnknownStapleCategory = errors.New("unknown staple category")

// StapleCategory identifies one group of common staples the deck builder can add in
// one click. Like the land categories, each group is a fixed card list filtered
// against the commander's color identity before it is offered.
type StapleCategory struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// StapleCategoryEntry is one staple offered for a chosen group, after color-identity
// filtering. Card carries the resolved Scryfall payload so the front-end can render
// image/oracle and classify it into construction metrics without a follow-up lookup.
type StapleCategoryEntry struct {
	Name        string           `json:"name"`
	Card        cardcatalog.Card `json:"card"`
	GameChanger bool             `json:"game_changer"`
}

// StapleCategoryResult is the set of staples the builder may add for one group.
type StapleCategoryResult struct {
	CategoryID    string                `json:"category_id"`
	CategoryLabel string                `json:"category_label"`
	Staples       []StapleCategoryEntry `json:"staples"`
}

// StapleCategories is the fixed catalog of common-staple groups, in the order the
// builder UI shows them. The first group ("common mana ramp") is a hand-curated list;
// the second ("可用的 Game Changer") is derived at request time from the official Game
// Changers list, filtered to the commander's color identity.
var StapleCategories = []StapleCategory{
	{ID: "ramp", Label: "常见法术力增长"},
	{ID: "game-changer", Label: "可用的 Game Changer"},
}

// stapleColors pairs a staple name with its color identity in compact canonical
// Scryfall order ("W", "U", "B", "R", "G"); an empty string means colorless, which is
// legal in any commander.
type stapleCard struct {
	name string
	ci   string
}

// staplePool is the unfiltered card list per group. Only the ramp group is defined
// for now: the ten Signets, the ten Talismans, three colorless mana rocks, and the
// classic green land-ramp spells.
var staplePool = map[string][]stapleCard{
	"ramp": {
		// 十种饰符（Signets）。
		{"Azorius Signet", "WU"}, {"Dimir Signet", "UB"}, {"Rakdos Signet", "BR"},
		{"Gruul Signet", "RG"}, {"Selesnya Signet", "GW"}, {"Orzhov Signet", "WB"},
		{"Izzet Signet", "UR"}, {"Golgari Signet", "BG"}, {"Boros Signet", "RW"},
		{"Simic Signet", "GU"},
		// 十种印记（Talismans）。五个单名印记实际都是友方双色（Conviction 实为
		// RW 等）；此处登记真实色组，且 BuildStaples 还会按解析后的 CI 二次过滤。
		{"Talisman of Conviction", "RW"}, {"Talisman of Curiosity", "GU"}, {"Talisman of Hierarchy", "WB"},
		{"Talisman of Impulse", "RG"}, {"Talisman of Resilience", "BG"}, {"Talisman of Dominance", "UB"},
		{"Talisman of Creativity", "UR"}, {"Talisman of Indulgence", "BR"}, {"Talisman of Progress", "WU"},
		{"Talisman of Unity", "GW"},
		// 无色法力石。
		{"Sol Ring", ""}, {"Arcane Signet", ""}, {"Fellwar Stone", ""},
		{"Thought Vessel", ""}, {"Thran Dynamo", ""},
		// 绿色找地加速。
		{"Cultivate", "G"}, {"Kodama's Reach", "G"}, {"Rampant Growth", "G"},
		{"Three Visits", "G"}, {"Nature's Lore", "G"}, {"Skyshroud Claim", "G"},
		// 绿色生物产费加速。
		{"Llanowar Elves", "G"}, {"Elvish Mystic", "G"}, {"Fyndhorn Elves", "G"},
		{"Birds of Paradise", "G"}, {"Llanowar Loamspeaker", "G"}, {"Devoted Druid", "G"},
		{"Noble Hierarch", "G"}, {"Elvish Archdruid", "G"}, {"Priest of Titania", "G"},
		{"Ilysian Caryatid", "G"}, {"Maraleaf Pixie", "G"}, {"Bloom Tender", "G"},
		{"Llanowar Tribe", "G"}, {"Selvala, Heart of the Wilds", "G"}, {"Circle of Dreams Druid", "G"},
		{"Krosan Restorer", "G"}, {"Argothian Elder", "G"}, {"Marwyn, the Nurturer", "G"},
		{"Rishkar, Peema Renegade", "G"}, {"Gwenna, Eyes of Gaea", "G"},
		{"Radha, Heir to Keld", "RG"}, {"Zhur-Taa Druid", "RG"},
		{"Kinnan, Bonder Prodigy", "GU"}, {"Grand Warlord Radha", "RG"}, {"Prime Speaker Vannifar", "GU"},
		{"Sakura-Tribe Elder", "G"}, {"Wood Elves", "G"}, {"Farhaven Elf", "G"},
		{"Llanowar Scout", "G"}, {"Growth Spasm", "G"}, {"Delighted Halfling", "G"},
		{"Fanatic of Rhonas", "G"}, {"Ragavan, Nimble Pilferer", "R"},
	},
}

// BuildStaples resolves every usable staple for one group and the given commander
// color identity, fetching each card's Scryfall payload so the front-end gets images
// and oracle without a follow-up call. Cards whose color identity falls outside the
// commander's are dropped; colorless staples are always kept. The game-changer group
// is derived from the official Game Changers list rather than a hand-curated pool.
func (a *Analyzer) BuildStaples(ctx context.Context, categoryID string, commanderIdentity []string) (StapleCategoryResult, error) {
	label := ""
	for _, category := range StapleCategories {
		if category.ID == categoryID {
			label = category.Label
			break
		}
	}
	if label == "" {
		return StapleCategoryResult{}, errUnknownStapleCategory
	}

	identity := map[string]struct{}{}
	for _, color := range commanderIdentity {
		if color == "" || color == "C" {
			continue
		}
		identity[color] = struct{}{}
	}

	var names []string
	seenNames := make(map[string]struct{})
	addName := func(name string) {
		key := strings.ToLower(strings.TrimSpace(name))
		if key == "" {
			return
		}
		// Defensive dedupe: the hand-curated pool once listed a card twice and
		// the duplicate surfaced as two identical buttons in the panel.
		if _, dup := seenNames[key]; dup {
			return
		}
		seenNames[key] = struct{}{}
		names = append(names, name)
	}
	if categoryID == "game-changer" {
		for name := range gameChangerNames {
			addName(name)
		}
	} else {
		for _, staple := range staplePool[categoryID] {
			if !colorsAllowed(colorLetters(staple.ci), identity) {
				continue
			}
			addName(staple.name)
		}
	}

	// A failed lookup may still carry every card that had a cache fallback
	// (cardcatalog serves stale disk entries on upstream failure); keep the
	// partial catalog so a flaky network shows fewer staples instead of none.
	catalog, _ := a.cards.Lookup(ctx, names)
	if catalog == nil {
		catalog = map[string]cardcatalog.Card{}
	}

	entries := make([]StapleCategoryEntry, 0, len(names))
	for _, name := range names {
		card, ok := catalog[strings.ToLower(strings.TrimSpace(name))]
		if !ok || !hasUsableCardData(card) {
			// A staple we could not resolve is skipped rather than shown broken.
			continue
		}
		// Color identity is re-checked against the resolved Scryfall data for
		// every group, not only game-changer: the pool's compact CI strings are
		// just a pre-filter and have been wrong before (the five allied
		// Talismans were once listed as single colors, leaking off-color cards
		// into mono-color commanders).
		if !colorsAllowed(card.ColorIdentity, identity) {
			continue
		}
		if categoryID == "game-changer" && !isGameChanger(card) {
			continue
		}
		entries = append(entries, StapleCategoryEntry{Name: card.Name, Card: card, GameChanger: isGameChanger(card)})
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return StapleCategoryResult{CategoryID: categoryID, CategoryLabel: label, Staples: entries}, nil
}
