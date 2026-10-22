package cardcatalog

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

const zhsSolRing = `{
	"object": "list", "has_more": false,
	"data": [{
		"oracle_id": "oracle-sol",
		"name": "Sol Ring",
		"printed_name": "太阳索指环",
		"type_line": "Artifact",
		"printed_type_line": "神器",
		"oracle_text": "{T}: Add {C}{C}.",
		"printed_text": "{T}：加{C}{C}。"
	}]
}`

func TestLookupChineseResolvesPrintAndCaches(t *testing.T) {
	var queries atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/cards/search" {
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
			return
		}
		if q := r.URL.Query().Get("q"); q != `!"Sol Ring" lang:zhs` {
			http.Error(w, "unexpected query "+q, http.StatusBadRequest)
			return
		}
		queries.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(zhsSolRing))
	}))
	defer server.Close()

	client := New(server.URL, server.Client(), time.Minute, t.TempDir())
	card, err := client.LookupChinese(context.Background(), "Sol Ring")
	if err != nil {
		t.Fatalf("LookupChinese: %v", err)
	}
	if card.Name != "Sol Ring" || card.ChineseName != "太阳索指环" ||
		card.ChineseTypeLine != "神器" || card.ChineseOracleText != "{T}：加{C}{C}。" {
		t.Fatalf("unexpected localized card: %+v", card)
	}
	// Same card through a differently-cased name: served from cache, one upstream hit total.
	again, err := client.LookupChinese(context.Background(), "sol ring")
	if err != nil {
		t.Fatalf("cached LookupChinese: %v", err)
	}
	if again.ChineseName != card.ChineseName {
		t.Fatalf("cache mismatch: %+v", again)
	}
	if got := queries.Load(); got != 1 {
		t.Fatalf("expected exactly one upstream zhs query, got %d", got)
	}
}

func TestLookupChineseCachesAcrossClientInstancesViaDisk(t *testing.T) {
	var queries atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		queries.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(zhsSolRing))
	}))
	defer server.Close()

	dir := t.TempDir()
	first := New(server.URL, server.Client(), time.Minute, dir)
	if _, err := first.LookupChinese(context.Background(), "Sol Ring"); err != nil {
		t.Fatal(err)
	}
	// A fresh client (process restart) with the same disk dir stays warm.
	second := New(server.URL, server.Client(), time.Minute, dir)
	card, err := second.LookupChinese(context.Background(), "Sol Ring")
	if err != nil {
		t.Fatal(err)
	}
	if card.ChineseName != "太阳索指环" {
		t.Fatalf("disk tier should serve the localization, got %+v", card)
	}
	if got := queries.Load(); got != 1 {
		t.Fatalf("disk tier should avoid a second upstream query, got %d", got)
	}
}

func TestLookupChineseNegativeCachesNoZhsPrinting(t *testing.T) {
	var queries atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		queries.Add(1)
		// Scryfall answers "no results" with a 404 + error object.
		http.Error(w, `{"object":"error","code":"not_found"}`, http.StatusNotFound)
	}))
	defer server.Close()

	client := New(server.URL, server.Client(), time.Minute, t.TempDir())
	card, err := client.LookupChinese(context.Background(), "Plains")
	if err != nil {
		t.Fatalf("missing zhs printing is a negative result, not an error: %v", err)
	}
	if card.ChineseName != "" || card.ChineseTypeLine != "" || card.ChineseOracleText != "" {
		t.Fatalf("expected empty localization fields, got %+v", card)
	}
	if _, err := client.LookupChinese(context.Background(), "Plains"); err != nil {
		t.Fatal(err)
	}
	if got := queries.Load(); got != 1 {
		t.Fatalf("negative result should be cached, got %d upstream queries", got)
	}
}

func TestLookupChinesePropagatesUpstreamErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	client := New(server.URL, server.Client(), time.Minute, t.TempDir())
	if _, err := client.LookupChinese(context.Background(), "Sol Ring"); err == nil {
		t.Fatal("upstream 503 must surface as an error, not an empty translation")
	}
}
