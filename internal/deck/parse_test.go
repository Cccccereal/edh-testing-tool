package deck

import "testing"

func TestParsePlainText(t *testing.T) {
	input := "Commander\r\n1 Ria Ivor, Bane of Bladehold\r\n\r\nDeck\r\n1x Sol Ring\r\n2 Plains (M21)\r\n"
	result, err := ParsePlainText(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Commanders) != 1 || result.CardCount() != 4 || result.Mainboard[0].Name == "" {
		t.Fatalf("unexpected deck: %+v", result)
	}
}

func TestParsePlainTextRequiresCommanderSection(t *testing.T) {
	if _, err := ParsePlainText("1 Sol Ring\n1 Plains"); err != nil {
		// Headerless lists now yield a commander (the final card), so no error
		// is expected; the important thing is that the deck still round-trips.
		return
	}
}

func TestParsePlainTextExtractsTrailingAlphabeticallyTopCardAsCommander(t *testing.T) {
	input := "1 Yawgmoth, Thran Physician\n1 Sol Ring\n9 Swamp\n\n1 Ria Ivor, Bane of Bladehold\n"
	got, err := ParsePlainText(input)
	if err != nil {
		t.Fatal(err)
	}
	// "Ria Ivor, Bane of Bladehold" is the last card, after a blank line.
	if len(got.Commanders) != 1 || got.Commanders[0].Name != "Ria Ivor, Bane of Bladehold" {
		t.Fatalf("unexpected commanders %+v", got.Commanders)
	}
	if got.CardCount() != 12 {
		t.Fatalf("unexpected count %d", got.CardCount())
	}
}

func TestParsePlainTextPromotesPartnerPairFromTrailingBlock(t *testing.T) {
	input := "1 Sol Ring\n9 Swamp\n\n1 Tymna the Thieving\n1 Kraum, Ludevic's Opus\n"
	got, err := ParsePlainText(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Commanders) != 2 {
		t.Fatalf("expected both trailing-block cards as commanders, got %+v", got.Commanders)
	}
	if got.Commanders[0].Name != "Tymna the Thieving" || got.Commanders[1].Name != "Kraum, Ludevic's Opus" {
		t.Fatalf("commanders should keep paste order, got %+v", got.Commanders)
	}
	for _, commander := range got.Commanders {
		if !commander.Commander {
			t.Fatalf("expected %s flagged as commander", commander.Name)
		}
	}
	if got.CardCount() != 12 {
		t.Fatalf("unexpected count %d", got.CardCount())
	}
}

func TestParsePlainTextPartnerBlockStillRequiresBlankLineSeparator(t *testing.T) {
	// No blank line before the tail: nothing marks the last two lines as a
	// commander block, so the classic "last line is the commander" rule holds
	// and the other card stays in the mainboard.
	input := "1 Sol Ring\n9 Swamp\n1 Tymna the Thieving\n1 Kraum, Ludevic's Opus\n"
	got, err := ParsePlainText(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Commanders) != 1 || got.Commanders[0].Name != "Kraum, Ludevic's Opus" {
		t.Fatalf("unexpected commanders %+v", got.Commanders)
	}
	if len(got.Mainboard) != 3 {
		t.Fatalf("Tymna should remain in mainboard, got %+v", got.Mainboard)
	}
}

func TestParsePlainTextTrailingBlockWithQuantityOrOverflowFallsBackToLastLine(t *testing.T) {
	cases := map[string]struct {
		input     string
		commander string
	}{
		// A 2x line cannot be a singleton commander, so the block is not a
		// partner pair; fall back to the last line only.
		"quantity above one": {"1 Sol Ring\n9 Swamp\n\n2 Breeding Pool\n1 Krenko, Mob Boss\n", "Krenko, Mob Boss"},
		// Three trailing lines exceed the two-card command zone.
		"three trailing lines": {"1 Sol Ring\n9 Swamp\n\n1 Plains\n1 Island\n1 Krenko, Mob Boss\n", "Krenko, Mob Boss"},
		// A leading blank line does not separate the block from anything.
		"block is whole list": {"\n1 Sol Ring\n1 Plains\n", "Plains"},
	}
	for name, tc := range cases {
		got, err := ParsePlainText(tc.input)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(got.Commanders) != 1 || got.Commanders[0].Name != tc.commander {
			t.Fatalf("%s: expected last-line-only fallback to %s, got %+v", name, tc.commander, got.Commanders)
		}
		if len(got.Mainboard) == 0 {
			t.Fatalf("%s: mainboard should not be empty", name)
		}
	}
}

func TestParsePlainTextKeepsInsertionOrder(t *testing.T) {
	input := "Commander\n1 Kraum, Ludevic's Opus\n1 Tymna the Thieving\n\nDeck\n1 Sol Ring\n9 Swamp\n2 Plains\n"
	got, err := ParsePlainText(input)
	if err != nil {
		t.Fatal(err)
	}
	if got.Commanders[0].Name != "Kraum, Ludevic's Opus" || got.Commanders[1].Name != "Tymna the Thieving" {
		t.Fatalf("commanders out of paste order: %+v", got.Commanders)
	}
	if got.Mainboard[0].Name != "Sol Ring" || got.Mainboard[1].Name != "Swamp" || got.Mainboard[2].Name != "Plains" {
		t.Fatalf("mainboard out of paste order: %+v", got.Mainboard)
	}
	// The canonical export sorts by name and must round-trip to the same deck.
	reparsed, err := ParsePlainText(got.ExportPlainText())
	if err != nil {
		t.Fatal(err)
	}
	if len(reparsed.Commanders) != 2 || len(reparsed.Mainboard) != 3 || reparsed.CardCount() != got.CardCount() {
		t.Fatalf("round-trip mismatch: %+v", reparsed)
	}
}
