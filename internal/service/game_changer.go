package service

import (
	"powerlevel/internal/providers/cardcatalog"
)

// isGameChanger reports whether a card is on the Commander Game Changers list.
//
// The source of truth is Scryfall's official `game_changer` field, which Wizards
// maintains alongside the Commander Brackets list (it changes every few months).
// Two paths feed it:
//
//   - The cardcatalog provider records `game_changer: true` on every Scryfall card
//     that carries the flag, and Lookup-based flows (analysis, swap, builder pools)
//     read it straight off the catalog card. That is the primary path and never
//     needs a snapshot.
//   - The in-process EDH Power Level score uses getcards' own `gamechanger` flag.
//
// The hardcoded name list below is retained only as a last-resort fallback for
// cards that predate Scryfall's field or that arrive through a provider which does
// not expose it — it is intentionally a snapshot and can go stale, which is why it
// is never consulted before the live field.
func isGameChanger(card cardcatalog.Card) bool {
	if card.GameChanger {
		return true
	}
	for _, face := range card.Faces {
		if face.GameChanger {
			return true
		}
	}
	if _, ok := gameChangerNames[normalizeCardName(card.Name)]; ok {
		return true
	}
	for _, face := range card.Faces {
		if _, ok := gameChangerNames[normalizeCardName(face.Name)]; ok {
			return true
		}
	}
	return false
}

// gameChangerByName checks the snapshot list by name alone. It is the path for
// deck entries the catalog could not resolve (Scryfall down, card unknown) —
// the live `game_changer` field always wins when a catalog card is available.
func gameChangerByName(name string) bool {
	_, ok := gameChangerNames[normalizeCardName(name)]
	return ok
}

// gameChangerNames is the Commander format's official Game Changers list snapshot
// (the 53 cards from the initial Brackets announcement). It is a fallback only:
// cards flagged `game_changer` by Scryfall take precedence, and the list here may
// lag newer additions. Names are normalized via normalizeCardName so a split or
// multi-face card matches its front face too.
var gameChangerNames = func() map[string]struct{} {
	names := []string{
		"Ad Nauseam",
		"Ancient Tomb",
		"Aura Shards",
		"Biorhythm",
		"Bolas's Citadel",
		"Braids, Cabal Minion",
		"Chrome Mox",
		"Coalition Victory",
		"Consecrated Sphinx",
		"Crop Rotation",
		"Cyclonic Rift",
		"Demonic Tutor",
		"Drannith Magistrate",
		"Enlightened Tutor",
		"Farewell",
		"Field of the Dead",
		"Fierce Guardianship",
		"Force of Will",
		"Gaea's Cradle",
		"Gamble",
		"Gifts Ungiven",
		"Glacial Chasm",
		"Grand Arbiter Augustin IV",
		"Grim Monolith",
		"Humility",
		"Imperial Seal",
		"Intuition",
		"Jeska's Will",
		"Lion's Eye Diamond",
		"Mana Vault",
		"Mishra's Workshop",
		"Mox Diamond",
		"Mystical Tutor",
		"Narset, Parter of Veils",
		"Natural Order",
		"Necropotence",
		"Notion Thief",
		"Opposition Agent",
		"Orcish Bowmasters",
		"Panoptic Mirror",
		"Rhystic Study",
		"Seedborn Muse",
		"Serra's Sanctum",
		"Smothering Tithe",
		"Survival of the Fittest",
		"Teferi's Protection",
		"Tergrid, God of Fright",
		"Thassa's Oracle",
		"The One Ring",
		"The Tabernacle at Pendrell Vale",
		"Underworld Breach",
		"Vampiric Tutor",
		"Worldly Tutor",
	}
	set := make(map[string]struct{}, len(names))
	for _, name := range names {
		set[normalizeCardName(name)] = struct{}{}
	}
	return set
}()
