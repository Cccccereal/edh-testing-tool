package manabase

// ManaSource is a land or partial mana source and the colors it can produce. Weight
// allows discounting fragile or conditional sources per Karsten's counting rules
// (mana dork ≈ 0.5, Signet ≈ 0.75). Ported from DeckFlow.Core.Manabase.ManaSource.
type ManaSource struct {
	Name string

	// Produces is the set of colors this source can tap for.
	Produces []ManaColor

	// Weight is the effective source weight (1.0 for a normal land).
	Weight float64

	// IsLand is true when this source occupies a land slot (counts toward the
	// land-drop total), even when its color weight is discounted.
	IsLand bool

	// EntersUntapped is true when it can produce mana the turn it is played.
	EntersUntapped bool

	// ManaAmount is how much mana this source makes per activation (Sol Ring = 2).
	ManaAmount int

	// IsCommander is true for a source contributed by a command-zone card; such a
	// source is not drawn into the simulated library but still counts toward color
	// supply. The simplified stage-1 classifier does not populate this.
	IsCommander bool
}

// SpellRequirement is a colored spell whose castability we want to check. It is a
// trimmed port of DeckFlow.Core.Manabase.SpellRequirement.
type SpellRequirement struct {
	Name string

	// ManaValue is the total mana value — the turn the spell is cast on curve.
	ManaValue int

	// Pips holds colored pip counts by color (colors with zero pips are omitted).
	Pips map[ManaColor]int

	// IsGold is true when the card needs more than one color.
	IsGold bool

	// IsManaSource is true when the card is itself a mana rock/dork. Such cards are
	// excluded from castability rows but still feed the source pools.
	IsManaSource bool

	// IsPermanent is true for cards that stay on the battlefield (creature, artifact,
	// enchantment, planeswalker, battle); false for instant and sorcery.
	IsPermanent bool

	// IsCommander marks the deck's commander (pinned to the top of any listing).
	IsCommander bool

	// Quantity is copies of this spell in the deck (used by the mana curve).
	Quantity int
}

// ManabaseDeck is a fully classified deck ready for mana-base analysis: its lands,
// its colored spells, and the aggregate numbers the land-count formula needs.
// Ported from DeckFlow.Core.Manabase.ManasourceDeck, trimmed to stage-1 fields.
type ManabaseDeck struct {
	// TotalCards is the total cards including commanders (typically 100).
	TotalCards int

	// CommanderCount is commanders in the command zone.
	CommanderCount int

	// Sources is all lands / mana sources in the deck.
	Sources []ManaSource

	// Spells is colored spells whose castability we want to check.
	Spells []SpellRequirement

	// AverageManaValue is the mean mana value of the non-land cards.
	AverageManaValue float64

	// MedianManaValue is the median mana value of the non-land cards.
	MedianManaValue float64

	// AverageManaValueNoLands is the mean mana value counting lands as 0.
	AverageManaValueNoLands float64

	// MedianManaValueNoLands is the median mana value counting lands as 0.
	MedianManaValueNoLands float64

	// TotalManaValue is the sum of all non-land mana values (weighted by quantity).
	TotalManaValue int

	// RampAndDrawUnderThree is the count of ramp/card-draw spells of mana value 2
	// or less (the −0.28 land-target credit input).
	RampAndDrawUnderThree int

	// FastMana is the count of 0-cost mana artifacts (Lotus, Moxen).
	FastMana int

	// IsSingleton is true for a Commander deck (uses the 99-card formula).
	IsSingleton bool
}

// ColorFinding reports one color's source supply versus its toughest requirement in
// the deck. Ported from DeckFlow.Core.Manabase.ColorSourceFinding, trimmed.
type ColorFinding struct {
	// Color is the color examined.
	Color ManaColor `json:"color"`

	// ActualSources is effective sources of this color currently in the deck (weighted).
	ActualSources float64 `json:"actual_sources"`

	// RequiredSources is sources required by the most demanding spell of this color.
	RequiredSources int `json:"required_sources"`

	// DrivingSpell is the spell that drove the requirement (the worst single-spell deficit).
	DrivingSpell string `json:"driving_spell"`
}

// CostCount is one bucket of the mana curve: how many non-land, non-commander cards
// sit at a given mana value. Mana value 7 is the "7+" catch-all bucket.
type CostCount struct {
	ManaValue int    `json:"mana_value"`
	Label     string `json:"label"`
	Count     int    `json:"count"`

	// PermanentCount is the subset of Count that is a permanent (creature, artifact,
	// enchantment, planeswalker, battle — anything that stays on the battlefield).
	// The front-end draws it as the shorter back bar of a double-series stack so the
	// curve shows both total mana demand and its permanent portion at a glance.
	PermanentCount int `json:"permanent_count,omitempty"`
}

// Report is the trimmed mana-base report: land count, ramp, per-color sources, and
// a verdict. Ported from DeckFlow.Core.Manabase.ManasourceReport.
type Report struct {
	// ActualLands is lands actually in the deck.
	ActualLands int `json:"actual_lands"`

	// TargetLands is the Karsten-recommended land count for the curve.
	TargetLands float64 `json:"target_lands"`

	// LandDelta is actual minus target; negative means too few lands.
	LandDelta float64 `json:"land_delta"`

	// AverageManaValue is the mean non-land mana value the regression used.
	AverageManaValue float64 `json:"average_mana_value"`

	// RampAndDrawUnderThree is the ramp/draw credit input.
	RampAndDrawUnderThree int `json:"ramp_and_draw_under_three"`

	// FastMana is the 0-cost fast-mana credit input.
	FastMana int `json:"fast_mana"`

	// MedianManaValue is the median mana value of the non-land cards.
	MedianManaValue float64 `json:"median_mana_value"`

	// AverageManaValueNoLands is the mean mana value counting lands as 0.
	AverageManaValueNoLands float64 `json:"average_mana_value_no_lands"`

	// MedianManaValueNoLands is the median mana value counting lands as 0.
	MedianManaValueNoLands float64 `json:"median_mana_value_no_lands"`

	// TotalManaValue is the sum of all non-land mana values (weighted by quantity).
	TotalManaValue int `json:"total_mana_value"`

	// CostCounts is the mana curve: non-land spell counts by mana value. Each bucket
	// also carries the permanent-only subset (see CostCount.PermanentCount).
	CostCounts []CostCount `json:"cost_counts"`

	// ColorPips is the color composition of the deck's spells: the total number of
	// each colored mana pip (W/U/B/R/G) across non-land cards. It feeds the 法术力构成
	// visualization, mirroring the per-color source demand in a raw pip count.
	ColorPips map[string]int `json:"color_pips"`

	// ColorFindings is per-color source findings. Stage 1 keeps deck order; no
	// composite tail-risk ordering is applied.
	ColorFindings []ColorFinding `json:"color_findings"`
}
