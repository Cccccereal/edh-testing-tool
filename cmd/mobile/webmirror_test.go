package mobile

import (
	"bytes"
	"flag"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// The Android WebView loads the same frontend as the browser; cmd/mobile/web is
// a manual mirror of cmd/server/web. History shows hand-syncing drifts (the
// 2026-09-16 clear-button fix nearly shipped without it), so CI fails when the
// two directories diverge. The comparison is recursive: cmd/server/web/fonts/
// (the self-hosted display font) once shipped only to the desktop and 404'd on
// Android forever, because the old test skipped directories on both sides.
// After changing the frontend, run
//
//	go test ./cmd/mobile -run TestWebMirrorMatchesServerWeb -update
//
// to mirror the canonical files over before committing.
var mirrorUpdate = flag.Bool("update", false, "rewrite cmd/mobile/web from cmd/server/web")

// tree reads every regular file under root into {relative path: content}.
// Path separators are normalized to "/" so the assertion is stable on Windows.
func tree(t *testing.T, root string) map[string][]byte {
	t.Helper()
	files := map[string][]byte{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(rel)] = data
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return files
}

func TestWebMirrorMatchesServerWeb(t *testing.T) {
	serverDir := filepath.Join("..", "server", "web")
	mobileDir := "web"

	if *mirrorUpdate {
		if err := syncMirror(serverDir, mobileDir); err != nil {
			t.Fatalf("mirror update: %v", err)
		}
	}

	want := tree(t, serverDir)
	got := tree(t, mobileDir)

	for rel, serverBytes := range want {
		mobileBytes, ok := got[rel]
		if !ok {
			t.Errorf("cmd/server/web/%s exists but cmd/mobile/web/%s is missing; run `go test ./cmd/mobile -run TestWebMirrorMatchesServerWeb -update`", rel, rel)
			continue
		}
		if !bytes.Equal(serverBytes, mobileBytes) {
			t.Errorf("cmd/mobile/web/%s drifted from cmd/server/web/%s; run `go test ./cmd/mobile -run TestWebMirrorMatchesServerWeb -update`", rel, rel)
		}
	}
	for rel := range got {
		if _, ok := want[rel]; !ok {
			t.Errorf("cmd/mobile/web/%s has no counterpart in cmd/server/web; the mirror must stay a strict copy", rel)
		}
	}
}

func TestMain(m *testing.M) {
	flag.Parse()
	if *mirrorUpdate {
		if err := syncMirror(filepath.Join("..", "server", "web"), "web"); err != nil {
			os.Stderr.WriteString(err.Error() + "\n")
			os.Exit(1)
		}
	}
	os.Exit(m.Run())
}

// syncMirror makes mobileDir a strict copy of serverDir: every file and
// directory is copied over, and entries that no longer exist in serverDir are
// removed so stale assets cannot linger on the mobile side.
func syncMirror(serverDir, mobileDir string) error {
	entries, err := os.ReadDir(serverDir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		from := filepath.Join(serverDir, entry.Name())
		to := filepath.Join(mobileDir, entry.Name())
		if err := os.RemoveAll(to); err != nil {
			return err
		}
		if err := copyPath(from, to); err != nil {
			return err
		}
	}
	// Remove stale entries on the mobile side.
	mobileEntries, err := os.ReadDir(mobileDir)
	if err != nil {
		return err
	}
	for _, entry := range mobileEntries {
		if _, err := os.Stat(filepath.Join(serverDir, entry.Name())); err != nil {
			if err := os.RemoveAll(filepath.Join(mobileDir, entry.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

func copyPath(from, to string) error {
	info, err := os.Stat(from)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		data, err := os.ReadFile(from)
		if err != nil {
			return err
		}
		return os.WriteFile(to, data, 0o644)
	}
	if err := os.MkdirAll(to, 0o755); err != nil {
		return err
	}
	children, err := os.ReadDir(from)
	if err != nil {
		return err
	}
	for _, child := range children {
		if err := copyPath(filepath.Join(from, child.Name()), filepath.Join(to, child.Name())); err != nil {
			return err
		}
	}
	return nil
}
