package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
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
type imageDiskCache struct{ dir string }

const (
	imageMaxSize     = 10 << 20
	imageFetchErrors = 2 // attempts for transport-level failures only
)

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
	}
}

func (d *imageDiskCache) path(key string) string {
	// The full key (query included) feeds the hash so version bumps get their
	// own slot; the extension comes from the query-stripped path, since a '?'
	// is legal in a Scryfall URL but not in a filename.
	ext := strings.ToLower(filepath.Ext(strings.SplitN(key, "?", 2)[0]))
	sum := sha256.Sum256([]byte(key))
	return filepath.Join(d.dir, hex.EncodeToString(sum[:])+ext)
}
