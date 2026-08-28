package deck

import "testing"

func TestFormatPlainText(t *testing.T) {
	t.Parallel()
	got := FormatPlainText(
		[]Card{{Name: "Ria Ivor, Bane of Bladehold", Quantity: 1}},
		[]Card{{Name: "Swamp", Quantity: 9}, {Name: "Arcane Signet", Quantity: 1}},
	)
	want := "1 Ria Ivor, Bane of Bladehold\n\n1 Arcane Signet\n9 Swamp"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestExportPlainTextRoundTrips(t *testing.T) {
	t.Parallel()
	original := Deck{Commanders: []Card{{Name: "Zeta", Quantity: 1}, {Name: "Alpha", Quantity: 1}}, Mainboard: []Card{{Name: "Swamp", Quantity: 9}, {Name: "Arcane Signet", Quantity: 1}}}
	parsed, err := ParsePlainText(original.ExportPlainText())
	if err != nil {
		t.Fatalf("exported deck did not parse: %v", err)
	}
	if parsed.CardCount() != original.CardCount() || len(parsed.Commanders) != 2 {
		t.Fatalf("round trip mismatch: %+v", parsed)
	}
	// ParsePlainText assembles sections from maps, so card order is not
	// stable; compare name/quantity sets instead of positions.
	commanders := map[string]int{}
	for _, card := range parsed.Commanders {
		commanders[card.Name] = card.Quantity
	}
	mainboard := map[string]int{}
	for _, card := range parsed.Mainboard {
		mainboard[card.Name] = card.Quantity
	}
	if len(commanders) != 2 || commanders["Zeta"] != 1 || commanders["Alpha"] != 1 {
		t.Fatalf("round trip commander mismatch: %+v", parsed.Commanders)
	}
	if len(mainboard) != 2 || mainboard["Swamp"] != 9 || mainboard["Arcane Signet"] != 1 {
		t.Fatalf("round trip mainboard mismatch: %+v", parsed.Mainboard)
	}
}
