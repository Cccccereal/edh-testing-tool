package deck

import (
	"strings"
	"testing"
)

// FuzzParsePlainText asserts the pasted-decklist contract for arbitrary input:
// the parser never panics, any deck it accepts satisfies the documented bounds,
// and every accepted deck round-trips through ExportPlainText with the same
// cards (format_test.go pins this round-trip on fixed samples; fuzzing
// generalizes it to whatever users paste).
func FuzzParsePlainText(f *testing.F) {
	f.Add("Commander\r\n1 Ria Ivor, Bane of Bladehold\r\n\r\nDeck\r\n1x Sol Ring\r\n2 Plains (M21)\r\n")
	f.Add("1 Yawgmoth, Thran Physician\n1 Sol Ring\n9 Swamp\n\n1 Ria Ivor, Bane of Bladehold\n")
	f.Add("Commanders:\n1 Najeela, the Blade-Blossom\nSideboard\n1 Sol Ring\n")
	f.Add("Deck\n999 Burst\nCommander\n1 Foo\n")
	f.Add("999 Burst\n1 Burst\n1 Foo")
	f.Add("1 A (B) (C)\n1 Foo")
	f.Add("1 A/B\n1 C (SET)\n1 Foo")
	f.Add("# comment\n// note\n1 X // Y\n1 Foo")
	f.Add("")
	f.Add("\r\n\n")
	f.Add(strings.Repeat("1 Card\n", 1001))
	f.Fuzz(func(t *testing.T, input string) {
		result, err := ParsePlainText(input)
		if err != nil {
			return
		}
		if len(result.Commanders) == 0 {
			t.Fatalf("accepted deck without a commander")
		}
		if len(result.Mainboard) == 0 {
			t.Fatalf("accepted deck with an empty mainboard")
		}
		if result.CardCount() > 1000 {
			t.Fatalf("accepted deck with %d cards over the 1000 limit", result.CardCount())
		}
		for _, card := range result.Commanders {
			if !card.Commander {
				t.Fatalf("commander %q missing commander flag", card.Name)
			}
		}
		mainboardQuantities := map[string]int{}
		for _, card := range result.Mainboard {
			if card.Commander {
				t.Fatalf("mainboard card %q flagged as commander", card.Name)
			}
			if card.Quantity < 1 || card.Quantity > maxCardQuantity {
				t.Fatalf("mainboard card %q has quantity %d", card.Name, card.Quantity)
			}
			if card.Name == "" || len(card.Name) > maxCardNameLength {
				t.Fatalf("mainboard card has invalid name %q", card.Name)
			}
			mainboardQuantities[strings.ToLower(card.Name)] = card.Quantity
		}

		exported := result.ExportPlainText()
		reparsed, err := ParsePlainText(exported)
		if err != nil {
			t.Fatalf("export of an accepted deck does not re-parse: %v\ninput: %q\nexport: %q", err, input, exported)
		}
		if reparsed.CardCount() != result.CardCount() {
			t.Fatalf("round-trip changed card count %d -> %d\ninput: %q\nexport: %q", result.CardCount(), reparsed.CardCount(), input, exported)
		}
		if len(reparsed.Mainboard) != len(result.Mainboard) {
			t.Fatalf("round-trip changed mainboard size %d -> %d\ninput: %q\nexport: %q", len(result.Mainboard), len(reparsed.Mainboard), input, exported)
		}
		for _, card := range reparsed.Mainboard {
			if mainboardQuantities[strings.ToLower(card.Name)] != card.Quantity {
				t.Fatalf("round-trip changed mainboard card %q to quantity %d\ninput: %q\nexport: %q", card.Name, card.Quantity, input, exported)
			}
		}
	})
}
