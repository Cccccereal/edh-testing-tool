package cardcatalog

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// diskCache persists resolved cards as one JSON file per lookup key under a
// directory, so process restarts start warm and upstream outages degrade to
// slightly-old data instead of blank card payloads. Key material never becomes
// a filename directly — files are named by the SHA-256 of the normalized name,
// which sidesteps unicode, slashes ("Fire // Ice") and filesystem limits.
// All operations are best-effort: a broken or missing cache must never fail a
// request that the network could still serve.
type diskCache struct {
	dir string
}

// diskEntry is the on-disk envelope. FetchedAt drives both freshness (with the
// client TTL) and stale-on-error serving.
type diskEntry struct {
	FetchedAt time.Time `json:"fetched_at"`
	Card      Card      `json:"card"`
}

func newDiskCache(dir string) *diskCache {
	if dir == "" {
		return nil
	}
	cardsDir := filepath.Join(dir, "cards")
	// Created here once; a failure (read-only volume, sandboxed tests) just
	// disables the disk tier via the nil guard below.
	if err := os.MkdirAll(cardsDir, 0o755); err != nil {
		return nil
	}
	return &diskCache{dir: cardsDir}
}

// get returns the cached card for key, its fetch time, and whether it was found.
func (d *diskCache) get(key string) (Card, time.Time, bool) {
	if d == nil {
		return Card{}, time.Time{}, false
	}
	data, err := os.ReadFile(d.path(key))
	if err != nil {
		return Card{}, time.Time{}, false
	}
	var entry diskEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		// Corrupt entry (partial write, format change): drop it so the next
		// successful fetch can rewrite the slot.
		_ = os.Remove(d.path(key))
		return Card{}, time.Time{}, false
	}
	if entry.Card.Name == "" {
		return Card{}, time.Time{}, false
	}
	return entry.Card, entry.FetchedAt, true
}

// put writes the card atomically (temp file + rename) so a crash never leaves
// a half-written entry behind.
func (d *diskCache) put(key string, card Card, fetchedAt time.Time) {
	if d == nil {
		return
	}
	data, err := json.Marshal(diskEntry{FetchedAt: fetchedAt, Card: card})
	if err != nil {
		return
	}
	temp, err := os.CreateTemp(d.dir, "*.tmp")
	if err != nil {
		return
	}
	name := temp.Name()
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		os.Remove(name)
		return
	}
	if err := temp.Close(); err != nil {
		os.Remove(name)
		return
	}
	// Windows needs the target gone before renaming over it.
	_ = os.Remove(d.path(key))
	if err := os.Rename(name, d.path(key)); err != nil {
		os.Remove(name)
	}
}

func (d *diskCache) path(key string) string {
	sum := sha256.Sum256([]byte(key))
	return filepath.Join(d.dir, hex.EncodeToString(sum[:])+".json")
}

// diskKeys lists the lookup keys whose stored card can answer a request for
// key, most specific first. Mirrors the split-name fallbacks of Lookup: a deck
// entry "Fire // Ice" may have been stored under either the full name or a
// front face, depending on what Scryfall returned when it was fetched.
func diskKeys(key string) []string {
	seen := make(map[string]struct{})
	var keys []string
	add := func(name string) {
		if name == "" {
			return
		}
		if _, ok := seen[name]; ok {
			return
		}
		seen[name] = struct{}{}
		keys = append(keys, name)
	}
	add(key)
	add(normalizeSplitName(key))
	if index := strings.Index(key, " // "); index > 0 {
		add(key[:index])
	}
	if index := strings.Index(key, " /// "); index > 0 {
		add(key[:index])
	}
	return keys
}
