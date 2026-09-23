package cardcatalog

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestLookupFallsBackToNamedForNotFound pins the Sol Ring regression: when the
// collection endpoint answers not_found for a name that /cards/named resolves,
// Lookup must still return the card instead of silently dropping it.
func TestLookupFallsBackToNamedForNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/cards/collection":
			_, _ = w.Write([]byte(`{
				"not_found": [{"name": "Sol Ring"}],
				"data": [{
					"oracle_id": "oracle-thran",
					"name": "Thran Dynamo",
					"type_line": "Artifact",
					"oracle_text": "{T}: Add {C}{C}{C}."
				}]
			}`))
		case "/cards/named":
			if r.URL.Query().Get("exact") != "Sol Ring" {
				http.Error(w, "unexpected exact name", http.StatusBadRequest)
				return
			}
			_, _ = w.Write([]byte(`{
				"oracle_id": "oracle-sol",
				"name": "Sol Ring",
				"type_line": "Artifact",
				"oracle_text": "{T}: Add {C}{C}."
			}`))
		default:
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := New(server.URL, server.Client(), time.Minute, t.TempDir())
	catalog, err := client.Lookup(context.Background(), []string{"Sol Ring", "Thran Dynamo"})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	sol, ok := catalog["sol ring"]
	if !ok || sol.Name != "Sol Ring" {
		t.Fatalf("Sol Ring should resolve via the /cards/named fallback, got %+v (present=%v)", sol, ok)
	}
	if _, ok := catalog["thran dynamo"]; !ok {
		t.Fatal("Thran Dynamo should resolve from the collection response")
	}
}

// TestLookupDropsUnknownNamesQuietly verifies the fallback does not turn a
// genuinely unknown name into an error: the miss is skipped, the rest returns.
func TestLookupDropsUnknownNamesQuietly(t *testing.T) {
	namedCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/cards/collection":
			_, _ = w.Write([]byte(`{
				"not_found": [{"name": "A Card That Does Not Exist"}],
				"data": [{
					"oracle_id": "oracle-sol",
					"name": "Sol Ring",
					"type_line": "Artifact",
					"oracle_text": "{T}: Add {C}{C}."
				}]
			}`))
		case "/cards/named":
			namedCalls++
			http.Error(w, "not found", http.StatusNotFound)
		default:
			http.Error(w, "unexpected path", http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := New(server.URL, server.Client(), time.Minute, t.TempDir())
	catalog, err := client.Lookup(context.Background(), []string{"Sol Ring", "A Card That Does Not Exist"})
	if err != nil {
		t.Fatalf("Lookup should not error on an unknown name: %v", err)
	}
	if _, ok := catalog["sol ring"]; !ok {
		t.Fatal("known card must still resolve")
	}
	if _, ok := catalog["a card that does not exist"]; ok {
		t.Fatal("unknown name must stay absent")
	}
	if namedCalls != 1 {
		t.Fatalf("expected exactly one /cards/named fallback attempt, got %d", namedCalls)
	}
}
