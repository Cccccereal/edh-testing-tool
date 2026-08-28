package deck

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

const (
	maxDecklistBytes  = 256 << 10
	maxDecklistLines  = 1000
	maxCardNameLength = 240
	maxCardQuantity   = 999
)

// The quantity multiplier ("1x Sol Ring") must sit directly against the
// number. Allowing whitespace before it ("1 X ...") would eat the leading X
// of a card name and break re-parsing of our own export.
var cardLinePattern = regexp.MustCompile(`^\s*(\d{1,3})[xX]?\s+(.+?)\s*$`)

func ParsePlainText(input string) (Deck, error) {
	if len(input) > maxDecklistBytes {
		return Deck{}, errors.New("decklist is too large")
	}
	input = strings.ReplaceAll(input, "\r\n", "\n")
	lines := strings.Split(input, "\n")
	if len(lines) > maxDecklistLines {
		return Deck{}, errors.New("decklist has too many lines")
	}
	section := ""
	commanders := map[string]Card{}
	mainboard := map[string]Card{}
	var trailingLine string
	sawCommanderSection := false
	for number, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		switch strings.ToLower(strings.TrimSuffix(line, ":")) {
		case "commander", "commanders":
			section = "commander"
			sawCommanderSection = true
			continue
		case "deck", "mainboard", "main deck", "maindeck":
			section = "mainboard"
			continue
		case "sideboard", "maybeboard", "considering":
			section = "ignore"
			continue
		}
		match := cardLinePattern.FindStringSubmatch(line)
		if match == nil {
			return Deck{}, fmt.Errorf("invalid decklist line %d", number+1)
		}
		if section == "" {
			// Headerless format: everything is mainboard until the final line,
			// which is treated as the commander.
			section = "mainboard"
		}
		if section == "ignore" {
			continue
		}
		quantity, _ := strconv.Atoi(match[1])
		if quantity < 1 || quantity > maxCardQuantity {
			return Deck{}, fmt.Errorf("invalid quantity on line %d", number+1)
		}
		name := cleanImportedName(match[2])
		if name == "" || len(name) > maxCardNameLength {
			return Deck{}, fmt.Errorf("invalid card name on line %d", number+1)
		}
		trailingLine = name
		target := mainboard
		commander := false
		if section == "commander" {
			target = commanders
			commander = true
		}
		key := strings.ToLower(name)
		item := target[key]
		item.Name = name
		item.Quantity += quantity
		if item.Quantity > maxCardQuantity {
			return Deck{}, fmt.Errorf("invalid quantity on line %d", number+1)
		}
		item.Commander = commander
		target[key] = item
	}
	// The user's pasted format places the commander on its own line at the very
	// end, after a blank line, with no "Commander"/"Deck" section headers.
	if len(commanders) == 0 && !sawCommanderSection && trailingLine != "" {
		delete(mainboard, strings.ToLower(trailingLine))
		commanders[strings.ToLower(trailingLine)] = Card{Name: trailingLine, Quantity: 1, Commander: true}
	}
	if len(commanders) == 0 {
		return Deck{}, errors.New("decklist must include at least one commander")
	}
	if len(mainboard) == 0 {
		return Deck{}, errors.New("decklist mainboard is empty")
	}
	result := Deck{}
	for _, item := range commanders {
		result.Commanders = append(result.Commanders, item)
	}
	// Both sections are kept as pasted: a name may appear in both the
	// commander and deck sections, and dropping either copy would break the
	// export round-trip (ExportPlainText must re-parse to the same deck).
	for _, item := range mainboard {
		result.Mainboard = append(result.Mainboard, item)
	}
	if result.CardCount() > 1000 {
		return Deck{}, errors.New("decklist card count is too large")
	}
	return result, nil
}

func cleanImportedName(value string) string {
	value = strings.TrimSpace(value)
	// Fold DFC spellings before stripping printing suffixes: folding can
	// create a new " (" boundary ("0/()" -> "0 // ()"), and the result must
	// be a fixed point so re-parsing our own export changes nothing.
	value = normalizeSplit(value)
	for {
		index := strings.LastIndex(value, " (")
		if index <= 0 || !strings.HasSuffix(value, ")") {
			break
		}
		value = strings.TrimSpace(value[:index])
	}
	return value
}

// normalizeSplit folds single-slash DFC spellings like "X/Y" into the
// canonical "X // Y" separator Scryfall uses. Only interior slashes (with a
// real name character on both sides) fold, and all of them fold in one pass,
// so the result is idempotent: re-parsing our own export must not fold
// another layer out of the same name.
func normalizeSplit(value string) string {
	if !strings.Contains(value, "/") {
		return value
	}
	var builder strings.Builder
	builder.Grow(len(value) + 8)
	for index := 0; index < len(value); index++ {
		if value[index] != '/' {
			builder.WriteByte(value[index])
			continue
		}
		interior := index > 0 && index+1 < len(value) &&
			value[index-1] != '/' && value[index-1] != ' ' &&
			value[index+1] != '/' && value[index+1] != ' '
		if !interior {
			builder.WriteByte('/')
			continue
		}
		builder.WriteString(" // ")
	}
	return builder.String()
}
