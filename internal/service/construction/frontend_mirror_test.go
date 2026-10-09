package construction

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// TestFrontendBuildMetricsMirrorGoTargets keeps the Go targets table and the
// frontend's BUILD_METRICS (cmd/server/web/config.js) honest: the six metric
// ids the builder sidebar renders must exist here with identical targets.
// The frontend displays targets from config.js while the server computes gaps
// from Targets(); a drift makes the progress bars lie about the real weights.
func TestFrontendBuildMetricsMirrorGoTargets(t *testing.T) {
	path := filepath.Join("..", "..", "..", "cmd", "server", "web", "config.js")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read frontend config: %v", err)
	}
	pattern := regexp.MustCompile(`\{ id: '([a-z_]+)', label: '[^']*', target: (\d+) \}`)
	matches := pattern.FindAllStringSubmatch(string(data), -1)
	if len(matches) < 6 {
		t.Fatalf("parsed %d BUILD_METRICS entries from config.js, want >= 6", len(matches))
	}
	targets := Targets()
	seen := make(map[string]int, len(matches))
	for _, match := range matches {
		id := match[1]
		target := atoiHelper(match[2])
		if goTarget, known := targets[id]; !known {
			t.Fatalf("frontend metric %q has no Go counterpart in targets", id)
		} else if goTarget != target {
			t.Fatalf("metric %q: frontend target %d != Go target %d — update config.js or targets together", id, target, goTarget)
		}
		if prev, dup := seen[id]; dup {
			t.Fatalf("metric %q listed twice in config.js (previous %d)", id, prev)
		}
		seen[id] = target
	}
}

func atoiHelper(digits string) int {
	value := 0
	for _, d := range digits {
		value = value*10 + int(d-'0')
	}
	return value
}
