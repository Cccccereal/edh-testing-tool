package cardcatalog

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestMatchChineseRanksExactPrefixContainsFuzzy(t *testing.T) {
	entries := map[string]string{
		"闪电击":        "Lightning Bolt",
		"闪电扑击":       "Lightning Strike",
		"连锁闪电":       "Chain Lightning",
		"闪电奔行兽":      "Ball Lightning",
		"凯姆霸，瑞加斯统治者": "Kemba, Kha Regent",
	}
	// 完全相等排最前；子序列模糊命中仍可尾随
	got := matchChinese(entries, "闪电击", 12)
	if len(got) < 1 || got[0] != "Lightning Bolt" {
		t.Fatalf("exact match must rank first, got %v", got)
	}
	for _, name := range got[1:] {
		if name == "Lightning Bolt" {
			t.Fatalf("exact hit must not repeat, got %v", got)
		}
	}
	// 前缀命中排前，包含命中排后；同分按英文名稳定排序
	got = matchChinese(entries, "闪电", 12)
	want := []string{
		// 80 分组（前缀）按英文名排序，60 分（包含）垫底
		"Ball Lightning", "Lightning Bolt", "Lightning Strike", "Chain Lightning",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("prefix before contains, name-sorted inside tiers, got %v", got)
	}
	// 子序列模糊：漏字也能命中
	got = matchChinese(entries, "电击", 12)
	if len(got) == 0 || got[0] != "Lightning Bolt" {
		t.Fatalf("fuzzy subsequence should hit, got %v", got)
	}
	// limit 截断
	if got := matchChinese(entries, "闪电", 2); len(got) != 2 {
		t.Fatalf("limit must cap results, got %v", got)
	}
}

func TestMatchChineseKeepsBestScorePerCard(t *testing.T) {
	// 同一张牌的两个中文别名：一个完全相等、一个仅包含，必须取高分而不是被低分覆盖
	entries := map[string]string{
		"密探":   "Enigmatic Incarnation",
		"探王密探": "Enigmatic Incarnation",
	}
	got := matchChinese(entries, "密探", 12)
	if len(got) != 1 || got[0] != "Enigmatic Incarnation" {
		t.Fatalf("per-card best score expected, got %v", got)
	}
}

func TestDeriveByEnPicksStableAlias(t *testing.T) {
	// 双面牌两个译名同属一张牌：反查表取字典序最小者，且与遍历顺序无关
	byEn := deriveByEn(map[string]string{
		"破断": "Break // Breakdown",
		"暴袭": "Break // Breakdown",
	})
	if byEn["Break // Breakdown"] != "暴袭" {
		t.Fatalf("smallest alias expected, got %q", byEn["Break // Breakdown"])
	}
}

// TestLookupFaceNameDoesNotPoisonRealCard 锁定回归：双面牌 "A // Lightning Bolt"
// 的面名键不得抢占真闪电击的缓存键，否则按名查闪电击会返回双面牌。
func TestLookupFaceNameDoesNotPoisonRealCard(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/cards/collection" {
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"data": [
			{"oracle_id": "oracle-bolt", "name": "Lightning Bolt", "type_line": "Instant", "oracle_text": "Deal 3."},
			{"oracle_id": "oracle-emeritus", "name": "Emeritus of Conflict // Lightning Bolt", "layout": "split", "type_line": "Instant // Instant",
			 "card_faces": [
				{"name": "Emeritus of Conflict", "type_line": "Instant", "oracle_text": "Do a thing."},
				{"name": "Lightning Bolt", "type_line": "Instant", "oracle_text": "Deal 3."}
			 ]}
		]}`))
	}))
	defer server.Close()

	client := New(server.URL, server.Client(), time.Minute, t.TempDir())
	catalog, err := client.Lookup(context.Background(), []string{"Lightning Bolt", "Emeritus of Conflict // Lightning Bolt"})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if bolt := catalog["lightning bolt"]; bolt.Name != "Lightning Bolt" {
		t.Fatalf("face-name key must not overwrite the real card, got %+v", bolt)
	}
}

func TestZhIndexRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "zh-index.json")
	idx := zhIndex{BuiltAt: time.Now(), Entries: map[string]string{"闪电击": "Lightning Bolt"}}
	if err := writeZhIndex(path, idx); err != nil {
		t.Fatalf("writeZhIndex: %v", err)
	}
	got, ok := readZhIndex(path, 24*time.Hour)
	if !ok || got.Entries["闪电击"] != "Lightning Bolt" {
		t.Fatalf("roundtrip lost entries: %+v ok=%v", got, ok)
	}
	// 过期索引不可用
	if _, ok := readZhIndex(path, time.Nanosecond); ok {
		t.Fatal("stale index must be rejected")
	}
	// 损坏文件不可用
	if err := os.WriteFile(path, []byte("{broken"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := readZhIndex(path, 24*time.Hour); ok {
		t.Fatal("corrupt index must be rejected")
	}
}
