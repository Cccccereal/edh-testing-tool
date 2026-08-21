package service

import (
	"context"
	"sort"

	"powerlevel/internal/providers/cardcatalog"
)

// LandCategory identifies one of the "一键出地" land cycles the deck builder can
// fill a draft with. Each category is a fixed list of lands; the builder filters
// each list against the commander's color identity before offering it.
type LandCategory struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// FetchLand represents a fetch land by the two basic-land types it can find, so the
// builder can filter it against the commander's colors even though the land itself
// has an empty color identity.
type FetchLand struct {
	Name     string `json:"name"`
	FetchesA string `json:"-"`
	FetchesB string `json:"-"`
}

// LandCategoryEntry is one land offered to the builder for a chosen category, after
// color-identity filtering. Card carries the resolved Scryfall payload (image,
// oracle, etc.) so the front-end can render it without a second lookup.
type LandCategoryEntry struct {
	Name string           `json:"name"`
	Card cardcatalog.Card `json:"card"`
}

// LandCategoryResult is the set of lands the builder may add for one category.
type LandCategoryResult struct {
	CategoryID    string              `json:"category_id"`
	CategoryLabel string              `json:"category_label"`
	Lands         []LandCategoryEntry `json:"lands"`
}

// LandCategories is the fixed catalog of land cycles, in the order the builder UI
// shows them. Each entry lists card names plus a color identity (or, for fetch
// lands, the two basic-land types it finds) used only for filtering; the names are
// resolved through Scryfall at request time so images and oracle stay current.
var LandCategories = []LandCategory{
	{ID: "shock", Label: "电震"},
	{ID: "surveil", Label: "刺探"},
	{ID: "original_dual", Label: "老圈"},
	{ID: "verge", Label: "边陲"},
	{ID: "scry", Label: "占卜地"},
	{ID: "multiplayer", Label: "多人地"},
	{ID: "fetch", Label: "找地"},
	{ID: "triome", Label: "三色圈"},
	{ID: "check", Label: "检查地"},
	{ID: "reveal", Label: "展示地"},
	{ID: "slow", Label: "慢地"},
}

// landPool maps a category id to its full, unfiltered card list. Color identity is
// expressed as the canonical Scryfall color letters ("W", "U", "B", "R", "G").
var landPool = map[string][]colorLand{
	"shock": {
		{"Hallowed Fountain", "UW"}, {"Watery Grave", "UB"}, {"Blood Crypt", "BR"},
		{"Stomping Ground", "RG"}, {"Temple Garden", "GW"}, {"Godless Shrine", "WB"},
		{"Sacred Foundry", "RW"}, {"Breeding Pool", "GU"}, {"Overgrown Tomb", "BG"},
		{"Steam Vents", "UR"},
	},
	"surveil": {
		{"Thundering Falls", "UR"}, {"Meticulous Archive", "WU"}, {"Shadowy Backstreet", "WB"},
		{"Undercity Sewers", "UB"}, {"Underground Mortuary", "BG"}, {"Raucous Theater", "BR"},
		{"Commercial District", "RG"}, {"Lush Portico", "GW"}, {"Elegant Parlor", "RW"},
		{"Hedge Maze", "GU"},
	},
	"original_dual": {
		{"Tundra", "WU"}, {"Underground Sea", "UB"}, {"Badlands", "BR"},
		{"Taiga", "RG"}, {"Savannah", "GW"}, {"Scrubland", "WB"},
		{"Volcanic Island", "UR"}, {"Bayou", "BG"}, {"Plateau", "RW"},
		{"Tropical Island", "GU"},
	},
	"verge": {
		{"Bleachbone Verge", "WB"}, {"Riverpyre Verge", "RU"}, {"Sunbillow Verge", "RW"},
		{"Wastewood Verge", "BG"}, {"Blazemire Verge", "BR"}, {"Willowrush Verge", "GU"},
		{"Floodfarm Verge", "UW"}, {"Gloomlake Verge", "BU"}, {"Hushwood Verge", "GW"},
		{"Thornspire Verge", "GR"},
	},
	"scry": {
		{"Temple of Abandon", "RG"}, {"Temple of Deceit", "BU"}, {"Temple of Enlightenment", "UW"},
		{"Temple of Epiphany", "RU"}, {"Temple of Malady", "BG"}, {"Temple of Malice", "BR"},
		{"Temple of Mystery", "GU"}, {"Temple of Plenty", "GW"}, {"Temple of Silence", "WB"},
		{"Temple of Triumph", "RW"},
	},
	"multiplayer": {
		{"Bountiful Promenade", "GW"}, {"Luxury Suite", "BR"}, {"Morphic Pool", "BU"},
		{"Rejuvenating Springs", "GU"}, {"Sea of Clouds", "UW"}, {"Spectator Seating", "RW"},
		{"Spire Garden", "GR"}, {"Training Center", "RU"}, {"Undergrowth Stadium", "BG"},
		{"Vault of Champions", "WB"},
	},
	"triome": {
		// New Capenna tri-lands.
		{"Jetmir's Garden", "GRW"}, {"Raffine's Tower", "BUW"}, {"Spara's Headquarters", "GUW"},
		{"Xander's Lounge", "BUR"}, {"Ziatora's Proving Ground", "BGR"},
		// Ikoria triomes.
		{"Indatha Triome", "BGW"}, {"Ketria Triome", "GRU"}, {"Raugrin Triome", "RUW"},
		{"Savai Triome", "BRW"}, {"Zagoth Triome", "BGU"},
	},
	// 操控两个基本地即可竖进的双色地（check lands）。
	"check": {
		{"Glacial Fortress", "WU"}, {"Drowned Catacomb", "UB"}, {"Dragonskull Summit", "BR"},
		{"Rootbound Crag", "RG"}, {"Sunpetal Grove", "GW"}, {"Isolated Chapel", "WB"},
		{"Clifftop Retreat", "RW"}, {"Hinterland Harbor", "GU"}, {"Woodland Cemetery", "BG"},
		{"Sulfur Falls", "UR"},
	},
	// 操控对应类别地即可竖进的双色地（reveal lands）。
	"reveal": {
		{"Prairie Stream", "WU"}, {"Sunken Hollow", "UB"}, {"Smoldering Marsh", "BR"},
		{"Cinder Glade", "RG"}, {"Canopy Vista", "GW"}, {"Fetid Pools", "WB"},
		{"Foreboding Ruins", "RW"}, {"Port Town", "GU"}, {"Choked Estuary", "BG"},
		{"Game Trail", "UR"},
	},
	// 基本地类别横进或慢地：竖进有条件的"慢圈"（slow lands）。
	"slow": {
		{"Deserted Beach", "WU"}, {"Shipwreck Marsh", "UB"}, {"Haunted Ridge", "BR"},
		{"Rockfall Vale", "RG"}, {"Overgrown Farmland", "GW"}, {"Frostboil Snarl", "UR"},
		{"Shadows' Verge", "WB"}, {"Sundown Pass", "RW"}, {"Dreamroot Cascade", "GU"},
		{"Deathcap Glade", "BG"},
	},
}

// colorLand pairs a land name with its color identity (or fetched basic-land types
// for fetch lands, which are filtered by those types instead).
type colorLand struct {
	name string
	ci   string
}

// Fetch lands filter on the basic-land types they find rather than color identity.
var fetchLands = []FetchLand{
	{"Arid Mesa", "Mountain", "Plains"},
	{"Marsh Flats", "Plains", "Swamp"},
	{"Misty Rainforest", "Forest", "Island"},
	{"Polluted Delta", "Island", "Swamp"},
	{"Verdant Catacombs", "Swamp", "Forest"},
	{"Wooded Foothills", "Mountain", "Forest"},
	{"Bloodstained Mire", "Swamp", "Mountain"},
	{"Flooded Strand", "Plains", "Island"},
	{"Scalding Tarn", "Island", "Mountain"},
	{"Windswept Heath", "Forest", "Plains"},
}

// BuildLands resolves every usable land for one category and the given commander
// color identity, fetching each land's Scryfall payload so the front-end gets
// images and oracle without a follow-up call. Lands whose colors fall outside the
// commander's identity are dropped; fetch lands are kept only when both of the
// basic-land types they find are inside the identity.
func (a *Analyzer) BuildLands(ctx context.Context, categoryID string, commanderIdentity []string) (LandCategoryResult, error) {
	label := ""
	for _, category := range LandCategories {
		if category.ID == categoryID {
			label = category.Label
			break
		}
	}
	if label == "" {
		return LandCategoryResult{}, errUnknownLandCategory
	}

	identity := map[string]struct{}{}
	for _, color := range commanderIdentity {
		if color == "" || color == "C" {
			continue
		}
		identity[color] = struct{}{}
	}

	var names []string
	if categoryID == "fetch" {
		for _, land := range fetchLands {
			fetchColors := []string{basicTypeColor(land.FetchesA), basicTypeColor(land.FetchesB)}
			if !anyColorAllowed(fetchColors, identity) {
				continue
			}
			names = append(names, land.Name)
		}
	} else {
		for _, land := range landPool[categoryID] {
			if !colorsAllowed(colorLetters(land.ci), identity) {
				continue
			}
			names = append(names, land.name)
		}
	}

	entries := make([]LandCategoryEntry, 0, len(names))
	for _, name := range names {
		card, err := a.LookupCard(ctx, name)
		if err != nil || !hasUsableCardData(card) {
			// A land we could not resolve is skipped rather than shown as a broken row.
			continue
		}
		entries = append(entries, LandCategoryEntry{Name: card.Name, Card: card})
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return LandCategoryResult{CategoryID: categoryID, CategoryLabel: label, Lands: entries}, nil
}

// colorLetters expands a compact color string like "UW" into ["W", "U"] in canonical
// Scryfall order. Unknown letters are dropped.
func colorLetters(compact string) []string {
	order := []byte{'W', 'U', 'B', 'R', 'G'}
	seen := make(map[byte]struct{}, len(compact))
	for _, needed := range compact {
		if _, ok := seen[byte(needed)]; ok {
			continue
		}
		seen[byte(needed)] = struct{}{}
	}
	out := make([]string, 0, len(compact))
	for _, color := range order {
		if _, ok := seen[color]; ok {
			out = append(out, string(color))
		}
	}
	return out
}

// anyColorAllowed reports whether at least one color in `colors` is inside `allowed`.
// Used for off-color fetch lands: a fetch is legal in a commander's identity if
// either basic-land type it fetches is in the identity, even when the other is not.
func anyColorAllowed(colors []string, allowed map[string]struct{}) bool {
	for _, color := range colors {
		if _, ok := allowed[color]; ok {
			return true
		}
	}
	return false
}

// basicTypeColor maps a basic-land type back to the color it represents for fetch
// filtering. Non-basic types (unused here) map to a never-matching placeholder.
func basicTypeColor(basicType string) string {
	switch basicType {
	case "Plains":
		return "W"
	case "Island":
		return "U"
	case "Swamp":
		return "B"
	case "Mountain":
		return "R"
	case "Forest":
		return "G"
	default:
		return "Z"
	}
}
