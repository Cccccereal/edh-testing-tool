package manabase

import (
	"regexp"
	"strings"
)

var manaSymbolRe = regexp.MustCompile(`\{([^}]+)\}`)

// manaCostParser parses Scryfall-style mana cost strings (e.g. "{2}{U}{U}") into
// mana value and hard colored pips. Hybrid ({U/R}), Phyrexian ({U/P}) and twobrid
// ({2/U}) symbols are deliberately not counted as hard single-color pips because
// they can be paid more than one way — Karsten counts them against combined
// sources. This mirrors DeckFlow's ManaCostParser.
type manaCostParser struct{}

type ParsedManaCost struct {
	ManaValue         int
	Pips              map[ManaColor]int
	HasVariableCost   bool
	TrueColorlessPips int
	SnowPips          int
}

func parseManaCost(cost string) ParsedManaCost {
	pips := make(map[ManaColor]int)
	parsed := ParsedManaCost{Pips: pips}
	if strings.TrimSpace(cost) == "" {
		return parsed
	}

	for _, match := range manaSymbolRe.FindAllStringSubmatch(cost, -1) {
		token := strings.ToUpper(strings.TrimSpace(match[1]))

		// Hybrid / Phyrexian / twobrid: no hard single-color pip. Twobrid ({2/U})
		// is worth 2 mana value; colored/Phyrexian hybrid ({U/R}, {U/P}) is worth 1.
		if strings.Contains(token, "/") {
			head := strings.Split(token, "/")[0]
			if n := atoi(head); n >= 0 {
				parsed.ManaValue += n
			} else if head == "2" {
				parsed.ManaValue += 2
			} else {
				parsed.ManaValue++
			}
			continue
		}

		if token == "X" || token == "Y" || token == "Z" {
			parsed.HasVariableCost = true
			continue
		}

		if n := atoi(token); n >= 0 {
			parsed.ManaValue += n
			continue
		}

		color, ok := mapSymbol(token)
		if !ok {
			continue
		}
		parsed.ManaValue++
		pips[color]++
		if token == "C" {
			parsed.TrueColorlessPips++
		} else if token == "S" {
			parsed.SnowPips++
		}
	}
	return parsed
}

// atoi parses a non-negative decimal integer, returning -1 on failure so callers
// can distinguish "generic cost N" from "not a number".
func atoi(s string) int {
	if s == "" {
		return -1
	}
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return -1
		}
		n = n*10 + int(r-'0')
	}
	return n
}
