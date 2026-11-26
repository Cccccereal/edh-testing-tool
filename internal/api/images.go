package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// imageProxy serves GET /img/<path> as a caching layer in front of Scryfall's
// image CDN. Card payloads already live behind the card catalog's disk cache,
// but the frontend pulls every card image straight from cards.scryfall.io —
// on a bad network path (the exact scenario customers hit) an analysis fills
// in text while its images trickle or break. Routing images through the local
// server gives them the same treatment the JSON gets: disk persistence across
// restarts, retries on transient upstream failures, and stale-on-error
// fallback so an outage shows last-known art instead of broken icons.
//
// This route is intentionally NOT a contract route: it is transport
// infrastructure shared by all clients, not part of the versioned API surface.
type imageProxy struct {
	baseURL    string
	httpClient *http.Client
	disk       *imageDiskCache
}

// imageDiskCache stores downloaded images as <dir>/<sha256(path)><ext>. The
// extension doubles as the content type on serve, so no metadata sidecar is
// needed; the hash sidesteps unicode/path issues the same way the card
// catalog's disk cache does.
type imageDiskCache struct {
	dir    string
	mu     sync.Mutex
	writes int
}

const (
	imageMaxSize     = 10 << 20
	imageFetchErrors = 2 // attempts for transport-level failures only

	// Scryfall 的缓存键是 ?<unix 秒> 这类短版本号。限制查询串的形状有两个目的：
	// 无法被用来把缓存键撑到任意长度；同一路径换查询串刷缓存的空间也随之收窄
	// （路径本身已按段白名单校验）。
	imageMaxQuery = 64

	// 磁盘上限：每次写入递增计数，到阈值做一次全目录清点，超过则按修改时间
	// 从旧到新删除。镜像没有索引文件，计数器重启归零——最坏情况是重启后第一次
	// 清理推迟到再写 500 张，可以接受。
	imageMaxFiles       = 20000
	imagePruneThreshold = 500
)

var imageQueryRe = regexp.MustCompile(`^[A-Za-z0-9._=&-]{1,` + fmt.Sprint(imageMaxQuery) + `}$`)

var imageMimeByExt = map[string]string{
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".png":  "image/png",
	".webp": "image/webp",
}

func newImageDiskCache(dir string) *imageDiskCache {
	if dir == "" {
		return nil
	}
	imagesDir := filepath.Join(dir, "images")
	if err := os.MkdirAll(imagesDir, 0o755); err != nil {
		return nil
	}
	return &imageDiskCache{dir: imagesDir}
}

// NewImageProxy builds the /img handler. An empty cacheDir (or an unwritable
// one) degrades to pass-through fetching — the proxy must never become the
// reason an image fails.
func NewImageProxy(baseURL string, httpClient *http.Client, cacheDir string) http.Handler {
	return &imageProxy{
		baseURL:    strings.TrimRight(baseURL, "/"),
		httpClient: httpClient,
		disk:       newImageDiskCache(cacheDir),
	}
}

func (p *imageProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rel, err := scryfallImageRelPath(r.URL.Path)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_IMAGE_PATH", "无效的图片路径。")
		return
	}
	ext := strings.ToLower(filepath.Ext(rel))
	// The CDN's ?<version> query is part of the cache identity: Scryfall bumps
	// it when art updates, and ignoring it would serve old art forever.
	if r.URL.RawQuery != "" && !imageQueryRe.MatchString(r.URL.RawQuery) {
		writeError(w, http.StatusBadRequest, "INVALID_IMAGE_QUERY", "无效的图片版本参数。")
		return
	}
	key := rel
	if r.URL.RawQuery != "" {
		key += "?" + r.URL.RawQuery
	}
	body, ok := p.disk.get(key)
	if !ok {
		body, err = p.fetch(r.Context(), key)
		if err != nil {
			// Upstream failed and nothing cached: report the failure as-is so
			// the browser's <img> fallback (onerror) still applies.
			writeError(w, http.StatusBadGateway, "IMAGE_UPSTREAM_FAILED", "暂时无法加载卡牌图片。")
			return
		}
		p.disk.put(key, body)
	}
	w.Header().Set("Content-Type", imageMimeByExt[ext])
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	w.Header().Set("Content-Length", fmt.Sprint(len(body)))
	_, _ = w.Write(body)
}

// fetch downloads one image with a bounded context and a single retry for
// transport errors / 5xx; client errors (404 etc.) are answers, not hiccups.
func (p *imageProxy) fetch(ctx context.Context, key string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var lastErr error
	for attempt := 0; attempt < imageFetchErrors; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(500 * time.Millisecond):
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/"+key, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", "PowerLevelAggregator/0.2")
		resp, err := p.httpClient.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, imageMaxSize+1))
		_ = resp.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}
		if len(data) > imageMaxSize {
			return nil, errors.New("Scryfall image response is too large")
		}
		if resp.StatusCode == http.StatusOK {
			return data, nil
		}
		lastErr = fmt.Errorf("Scryfall image returned HTTP %d", resp.StatusCode)
		if resp.StatusCode >= 500 {
			continue
		}
		return nil, lastErr
	}
	return nil, lastErr
}

// scryfallImageRelPath extracts and validates the path segment after /img/.
// Everything about it must look like a Scryfall CDN path (segments, dots,
// dashes, a known image extension); anything else — traversal, query tricks,
// absolute URLs — is rejected before it can become a filename or a request.
func scryfallImageRelPath(urlPath string) (string, error) {
	rel := strings.TrimPrefix(urlPath, "/img/")
	rel = strings.TrimPrefix(rel, "/")
	if rel == "" {
		return "", errors.New("empty image path")
	}
	ext := strings.ToLower(filepath.Ext(rel))
	if _, ok := imageMimeByExt[ext]; !ok {
		return "", fmt.Errorf("unsupported image extension %q", ext)
	}
	for _, segment := range strings.Split(rel, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return "", fmt.Errorf("bad path segment %q", segment)
		}
		for _, r := range segment {
			allowed := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' ||
				r == '.' || r == '-' || r == '_'
			if !allowed {
				return "", fmt.Errorf("bad character %q in image path", r)
			}
		}
	}
	return rel, nil
}

func (d *imageDiskCache) get(key string) ([]byte, bool) {
	if d == nil {
		return nil, false
	}
	data, err := os.ReadFile(d.path(key))
	if err != nil || len(data) == 0 {
		return nil, false
	}
	return data, true
}

func (d *imageDiskCache) put(key string, data []byte) {
	if d == nil {
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
		return
	}
	d.maybePrune()
}

// maybePrune runs the size-cap cleanup every imagePruneThreshold writes. Errors
// are swallowed: pruning is best-effort housekeeping and must never fail a
// request that just succeeded.
func (d *imageDiskCache) maybePrune() {
	d.mu.Lock()
	d.writes++
	due := d.writes%imagePruneThreshold == 0
	d.mu.Unlock()
	if !due {
		return
	}
	entries, err := os.ReadDir(d.dir)
	if err != nil || len(entries) <= imageMaxFiles {
		return
	}
	type stale struct {
		name    string
		modTime time.Time
	}
	items := make([]stale, 0, len(entries))
	for _, entry := range entries {
		if info, err := entry.Info(); err == nil {
			items = append(items, stale{entry.Name(), info.ModTime()})
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].modTime.Before(items[j].modTime) })
	for _, item := range items[:len(items)-imageMaxFiles] {
		_ = os.Remove(filepath.Join(d.dir, item.name))
	}
	slog.Info("image cache pruned", "removed", len(items)-imageMaxFiles, "kept", imageMaxFiles)
}

func (d *imageDiskCache) path(key string) string {
	// The full key (query included) feeds the hash so version bumps get their
	// own slot; the extension comes from the query-stripped path, since a '?'
	// is legal in a Scryfall URL but not in a filename.
	ext := strings.ToLower(filepath.Ext(strings.SplitN(key, "?", 2)[0]))
	sum := sha256.Sum256([]byte(key))
	return filepath.Join(d.dir, hex.EncodeToString(sum[:])+ext)
}
