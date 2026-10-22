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

// trailingEntry is one card line of the headerless tail block: the lines that
// follow the last blank line of the paste.
type trailingEntry struct {
	name     string
	quantity int
}

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
	commanderOrder := []string{}
	mainboard := map[string]Card{}
	mainboardOrder := []string{}
	// The tail block accumulates card lines; a blank line only "commits" the
	// separator when another card line follows, so blank lines at the very top
	// or bottom of the paste do not manufacture a block boundary.
	var block, prevBlock []trailingEntry
	blankPending := false
	sawCommanderSection := false
	for number, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" {
			blankPending = true
			continue
		}
		if strings.HasPrefix(line, "#") {
			continue
		}
		switch strings.ToLower(strings.TrimSuffix(line, ":")) {
		case "commander", "commanders":
			section = "commander"
			sawCommanderSection = true
			blankPending = false
			block, prevBlock = nil, nil
			continue
		case "deck", "mainboard", "main deck", "maindeck":
			section = "mainboard"
			blankPending = false
			block, prevBlock = nil, nil
			continue
		case "sideboard", "maybeboard", "considering":
			section = "ignore"
			blankPending = false
			block, prevBlock = nil, nil
			continue
		}
		match := cardLinePattern.FindStringSubmatch(line)
		if match == nil {
			return Deck{}, fmt.Errorf("invalid decklist line %d", number+1)
		}
		if blankPending {
			prevBlock = block
			block = nil
			blankPending = false
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
		block = append(block, trailingEntry{name: name, quantity: quantity})
		target := mainboard
		commander := false
		if section == "commander" {
			target = commanders
			commander = true
		}
		key := strings.ToLower(name)
		item := target[key]
		if item.Name == "" {
			if commander {
				commanderOrder = append(commanderOrder, key)
			} else {
				mainboardOrder = append(mainboardOrder, key)
			}
		}
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
	// 拍档主将（Partner 等双主将）会写成空行后的两行：整块都当主将候选。
	// 这里只做语法层的乐观提升——两行、各自数量 1、且空行前确实还有牌表；
	// 是否真是合法拍档由服务层拿卡牌资料校验（normalizeSuppliedCommanders），
	// 不合法就回退成"末行即主将"的旧口径。其余情况一律维持旧行为：只有
	// 最后一行是主将。
	if len(commanders) == 0 && !sawCommanderSection && len(block) > 0 {
		promoted := []string{block[len(block)-1].name}
		if len(prevBlock) > 0 && len(block) <= 2 {
			allSingleton := true
			for _, entry := range block {
				if entry.quantity != 1 {
					allSingleton = false
					break
				}
			}
			if allSingleton {
				promoted = promoted[:0]
				for _, entry := range block {
					promoted = append(promoted, entry.name)
				}
			}
		}
		for _, name := range promoted {
			key := strings.ToLower(name)
			delete(mainboard, key)
			for i, existing := range mainboardOrder {
				if existing == key {
					mainboardOrder = append(mainboardOrder[:i], mainboardOrder[i+1:]...)
					break
				}
			}
			if _, dup := commanders[key]; dup {
				continue
			}
			commanders[key] = Card{Name: name, Quantity: 1, Commander: true}
			commanderOrder = append(commanderOrder, key)
		}
	}
	if len(commanders) == 0 {
		return Deck{}, errors.New("decklist must include at least one commander")
	}
	if len(mainboard) == 0 {
		return Deck{}, errors.New("decklist mainboard is empty")
	}
	result := Deck{}
	// 插入顺序输出（而非 map 随机序）：让"最后一张主将"这类回退规则有确定的
	// 文本依据，API 响应里的主将顺序也不再逐次抖动。
	for _, key := range commanderOrder {
		result.Commanders = append(result.Commanders, commanders[key])
	}
	// Both sections are kept as pasted: a name may appear in both the
	// commander and deck sections, and dropping either copy would break the
	// export round-trip (ExportPlainText must re-parse to the same deck).
	for _, key := range mainboardOrder {
		result.Mainboard = append(result.Mainboard, mainboard[key])
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
