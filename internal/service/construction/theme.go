package construction

import (
	"strings"

	"powerlevel/internal/providers/cardcatalog"
)

// Theme represents the extracted strategic themes from a commander's abilities.
type Theme struct {
	// Keywords are the core mechanics extracted from the commander's oracle text.
	Keywords []string
	// CommanderText is the lowercased oracle text of the commander(s) for direct matching.
	CommanderText string
}

// ExtractTheme analyzes one or more commanders and extracts their core strategic themes.
// It identifies key mechanics, triggers, and synergy patterns from the oracle text.
func ExtractTheme(commanders []cardcatalog.Card) Theme {
	var fullText strings.Builder
	keywords := make(map[string]struct{})

	for _, cmd := range commanders {
		text := strings.ToLower(cmd.OracleText)
		fullText.WriteString(" ")
		fullText.WriteString(text)

		// Extract double-faced card text
		for _, face := range cmd.Faces {
			faceText := strings.ToLower(face.OracleText)
			fullText.WriteString(" ")
			fullText.WriteString(faceText)
			extractKeywords(faceText, keywords)
		}

		extractKeywords(text, keywords)
	}

	result := make([]string, 0, len(keywords))
	for kw := range keywords {
		result = append(result, kw)
	}

	return Theme{
		Keywords:      result,
		CommanderText: fullText.String(),
	}
}

// extractKeywords identifies strategic keywords and mechanics from oracle text.
func extractKeywords(text string, out map[string]struct{}) {
	// Counter-based strategies
	if strings.Contains(text, "+1/+1 counter") {
		out["+1/+1_counter"] = struct{}{}
	}
	if strings.Contains(text, "-1/-1 counter") {
		out["-1/-1_counter"] = struct{}{}
	}
	if strings.Contains(text, "counter") && (strings.Contains(text, "put") || strings.Contains(text, "move")) {
		out["counter_manipulation"] = struct{}{}
	}
	if strings.Contains(text, "proliferate") {
		out["proliferate"] = struct{}{}
	}

	// Token strategies
	if strings.Contains(text, "token") || strings.Contains(text, "create") && strings.Contains(text, "creature") {
		out["tokens"] = struct{}{}
	}

	// Sacrifice themes
	if strings.Contains(text, "sacrifice") {
		out["sacrifice"] = struct{}{}
	}

	// Graveyard strategies
	if strings.Contains(text, "graveyard") || strings.Contains(text, "dies") || strings.Contains(text, "when") && strings.Contains(text, "die") {
		out["graveyard"] = struct{}{}
	}
	if strings.Contains(text, "reanimate") || strings.Contains(text, "return") && strings.Contains(text, "from your graveyard") {
		out["reanimator"] = struct{}{}
	}

	// Card draw / advantage
	if strings.Contains(text, "whenever you draw") || strings.Contains(text, "draw a card") || strings.Contains(text, "draw cards") {
		out["card_draw"] = struct{}{}
	}

	// Equipment / Aura (Voltron)
	if strings.Contains(text, "equip") || strings.Contains(text, "equipment") {
		out["equipment"] = struct{}{}
	}
	if strings.Contains(text, "aura") || strings.Contains(text, "enchant creature") {
		out["auras"] = struct{}{}
	}
	if strings.Contains(text, "commander damage") {
		out["voltron"] = struct{}{}
	}

	// Spell-slinger
	if strings.Contains(text, "instant or sorcery") || strings.Contains(text, "cast an instant") || strings.Contains(text, "cast a sorcery") {
		out["spells"] = struct{}{}
	}

	// Tribal
	if strings.Contains(text, "creature you control") || strings.Contains(text, "creatures you control") {
		out["tribal"] = struct{}{}
	}

	// Energy
	if strings.Contains(text, "energy counter") || strings.Contains(text, "get {e}") {
		out["energy"] = struct{}{}
	}

	// Experience counters
	if strings.Contains(text, "experience counter") {
		out["experience"] = struct{}{}
	}

	// Landfall
	if strings.Contains(text, "landfall") || strings.Contains(text, "whenever a land enters") {
		out["landfall"] = struct{}{}
	}

	// Mill / self-mill
	if strings.Contains(text, "mill") || strings.Contains(text, "put the top") && strings.Contains(text, "into") && strings.Contains(text, "graveyard") {
		out["mill"] = struct{}{}
	}

	// Artifact synergies
	if strings.Contains(text, "artifact") && (strings.Contains(text, "you control") || strings.Contains(text, "enters")) {
		out["artifacts"] = struct{}{}
	}

	// Enchantment synergies
	if strings.Contains(text, "enchantment") && (strings.Contains(text, "you control") || strings.Contains(text, "enters")) {
		out["enchantments"] = struct{}{}
	}

	// Planeswalkers
	if strings.Contains(text, "planeswalker") {
		out["planeswalkers"] = struct{}{}
	}

	// Combat matters
	if strings.Contains(text, "combat") || strings.Contains(text, "attacking") || strings.Contains(text, "attacks") {
		out["combat"] = struct{}{}
	}

	// Life gain
	if strings.Contains(text, "gain") && strings.Contains(text, "life") || strings.Contains(text, "whenever you gain life") {
		out["lifegain"] = struct{}{}
	}

	// Infect / poison
	if strings.Contains(text, "infect") || strings.Contains(text, "poison counter") {
		out["poison"] = struct{}{}
	}

	// Storm / spell copies
	if strings.Contains(text, "storm") || strings.Contains(text, "copy") && strings.Contains(text, "spell") {
		out["spell_copy"] = struct{}{}
	}

	// Blink / flicker
	if strings.Contains(text, "exile") && strings.Contains(text, "return") && strings.Contains(text, "battlefield") {
		out["blink"] = struct{}{}
	}

	// +1/+1 counter support (broadly)
	if strings.Contains(text, "power") || strings.Contains(text, "toughness") || strings.Contains(text, "gets +") {
		out["buff"] = struct{}{}
	}
}

// MatchesPlan checks if a card aligns with the commander's extracted theme.
// It returns true and a reason if the card synergizes with the theme keywords
// or mentions similar mechanics.
func (t Theme) MatchesPlan(card cardcatalog.Card, synergy float64) (bool, string) {
	if synergy >= 0.30 {
		return true, "High EDHREC synergy with commander"
	}

	cardText := strings.ToLower(card.OracleText)
	cardType := strings.ToLower(card.TypeLine)
	for _, face := range card.Faces {
		cardText += " " + strings.ToLower(face.OracleText)
		cardType += " " + strings.ToLower(face.TypeLine)
	}

	// Check each extracted keyword theme
	for _, kw := range t.Keywords {
		switch kw {
		case "+1/+1_counter", "counter_manipulation", "proliferate":
			if strings.Contains(cardText, "counter") || strings.Contains(cardText, "proliferate") {
				return true, "Synergizes with commander's counter theme"
			}
		case "-1/-1_counter":
			if strings.Contains(cardText, "-1/-1") || strings.Contains(cardText, "wither") || strings.Contains(cardText, "infect") {
				return true, "Synergizes with -1/-1 counter theme"
			}
		case "tokens":
			if strings.Contains(cardText, "token") || strings.Contains(cardText, "create") && strings.Contains(cardText, "creature") {
				return true, "Creates or synergizes with tokens"
			}
		case "sacrifice":
			if strings.Contains(cardText, "sacrifice") {
				return true, "Sacrifice synergy"
			}
		case "graveyard", "reanimator":
			if strings.Contains(cardText, "graveyard") || strings.Contains(cardText, "dies") || strings.Contains(cardText, "return") && strings.Contains(cardText, "from") {
				return true, "Graveyard synergy"
			}
		case "card_draw":
			if strings.Contains(cardText, "draw") && !strings.Contains(cardText, "drawback") {
				return true, "Card draw synergy"
			}
		case "equipment":
			if strings.Contains(cardType, "equipment") || strings.Contains(cardText, "equip") {
				return true, "Equipment synergy"
			}
		case "auras":
			if strings.Contains(cardType, "aura") || strings.Contains(cardText, "enchant creature") {
				return true, "Aura synergy"
			}
		case "voltron":
			if strings.Contains(cardText, "equipped creature") || strings.Contains(cardText, "enchanted creature") {
				return true, "Voltron synergy"
			}
		case "spells":
			if strings.Contains(cardText, "instant") || strings.Contains(cardText, "sorcery") {
				return true, "Spell-slinger synergy"
			}
		case "tribal":
			if strings.Contains(cardText, "creature") && (strings.Contains(cardText, "you control") || strings.Contains(cardText, "enters")) {
				return true, "Tribal synergy"
			}
		case "energy":
			if strings.Contains(cardText, "energy") || strings.Contains(cardText, "{e}") {
				return true, "Energy synergy"
			}
		case "experience":
			if strings.Contains(cardText, "experience") {
				return true, "Experience counter synergy"
			}
		case "landfall":
			if strings.Contains(cardText, "landfall") || strings.Contains(cardText, "land enters") {
				return true, "Landfall synergy"
			}
		case "mill":
			if strings.Contains(cardText, "mill") || strings.Contains(cardText, "library") && strings.Contains(cardText, "graveyard") {
				return true, "Mill synergy"
			}
		case "artifacts":
			if strings.Contains(cardType, "artifact") || strings.Contains(cardText, "artifact") {
				return true, "Artifact synergy"
			}
		case "enchantments":
			if strings.Contains(cardType, "enchantment") || strings.Contains(cardText, "enchantment") {
				return true, "Enchantment synergy"
			}
		case "planeswalkers":
			if strings.Contains(cardType, "planeswalker") || strings.Contains(cardText, "planeswalker") {
				return true, "Planeswalker synergy"
			}
		case "combat":
			if strings.Contains(cardText, "combat") || strings.Contains(cardText, "attack") {
				return true, "Combat synergy"
			}
		case "lifegain":
			if strings.Contains(cardText, "gain") && strings.Contains(cardText, "life") {
				return true, "Life gain synergy"
			}
		case "poison":
			if strings.Contains(cardText, "poison") || strings.Contains(cardText, "infect") {
				return true, "Poison synergy"
			}
		case "spell_copy":
			if strings.Contains(cardText, "copy") || strings.Contains(cardText, "storm") {
				return true, "Spell copy synergy"
			}
		case "blink":
			if strings.Contains(cardText, "exile") && strings.Contains(cardText, "return") {
				return true, "Blink/flicker synergy"
			}
		case "buff":
			if strings.Contains(cardText, "gets +") || strings.Contains(cardText, "power") {
				return true, "Buff synergy"
			}
		}
	}

	// Fallback: generic "whenever" trigger cards
	if strings.Contains(cardText, "whenever") {
		return true, "Trigger-based synergy"
	}

	return false, ""
}
