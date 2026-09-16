package mobile

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"testing"
)

// The Android WebView loads the same frontend as the browser; cmd/mobile/web is
// a manual mirror of cmd/server/web. History shows hand-syncing drifts (the
// 2026-09-16 clear-button fix nearly shipped without it), so CI fails when the
// two directories diverge. After changing the frontend, run
//
//	go test ./cmd/mobile -run TestWebMirrorMatchesServerWeb -update
//
// to copy the canonical files over before committing.
var mirrorUpdate = flag.Bool("update", false, "rewrite cmd/mobile/web from cmd/server/web")

func TestWebMirrorMatchesServerWeb(t *testing.T) {
	serverDir := filepath.Join("..", "server", "web")
	mobileDir := "web"

	entries, err := os.ReadDir(serverDir)
	if err != nil {
		t.Fatalf("read server web dir: %v", err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		want, err := os.ReadFile(filepath.Join(serverDir, entry.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", entry.Name(), err)
		}
		mobilePath := filepath.Join(mobileDir, entry.Name())
		got, err := os.ReadFile(mobilePath)
		if err != nil {
			t.Fatalf("cmd/server/web/%s exists but cmd/mobile/web/%s is missing; run with -update: %v", entry.Name(), entry.Name(), err)
		}
		if !bytes.Equal(want, got) {
			t.Errorf("cmd/mobile/web/%s drifted from cmd/server/web/%s; run `go test ./cmd/mobile -run TestWebMirrorMatchesServerWeb -update`", entry.Name(), entry.Name())
		}
	}

	mobileEntries, err := os.ReadDir(mobileDir)
	if err != nil {
		t.Fatalf("read mobile web dir: %v", err)
	}
	for _, entry := range mobileEntries {
		if entry.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(serverDir, entry.Name())); err != nil {
			t.Errorf("cmd/mobile/web/%s has no counterpart in cmd/server/web; the mirror must stay a strict copy", entry.Name())
		}
	}
}

func TestMain(m *testing.M) {
	flag.Parse()
	if *mirrorUpdate {
		if err := syncMirror(); err != nil {
			os.Stderr.WriteString(err.Error() + "\n")
			os.Exit(1)
		}
	}
	os.Exit(m.Run())
}

func syncMirror() error {
	serverDir := filepath.Join("..", "server", "web")
	entries, err := os.ReadDir(serverDir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(serverDir, entry.Name()))
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join("web", entry.Name()), data, 0o644); err != nil {
			return err
		}
	}
	return nil
}
