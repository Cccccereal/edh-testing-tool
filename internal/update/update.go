// Package update checks GitHub for the newest published release so the UI can
// tell users an update exists. Both distribution channels (desktop client exe,
// Android APK) embed their own copy of the server and web assets, so there is
// no auto-update path — users install a new build manually; this package only
// answers "is there something newer, and where".
//
// Two lookup sources, both zero-maintenance:
//  1. api.github.com releases/latest JSON — richest (notes + asset URLs), but
//     api.github.com is the endpoint most often unreachable from CN networks.
//  2. github.com/…/releases/latest redirect — a HEAD request whose Location
//     header carries the tag; github.com itself is more reliably reachable.
//
// Results (and failures) are cached: success for 24h, failure for 1h, so the
// endpoint stays cheap and an offline network never turns into per-request
// latency. All failures are silent — the caller just gets nil and the UI shows
// nothing.
package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	repo = "Cccccereal/edh-testing-tool"

	successTTL   = 24 * time.Hour
	failureTTL   = time.Hour
	fetchTimeout = 5 * time.Second

	// Fixed release-asset names from .github/workflows/release.yml. The APK
	// name embeds the tag; the exe names are constant across releases.
	apkAssetFmt  = "edh-powerlevel-%s.apk"
	exeAssetName = "edh-powerlevel-client.exe"
)

// Info describes the newest published release. JSON shape is part of the API
// contract (docs/api/openapi.yaml, VersionResponse.latest).
type Info struct {
	Version    string `json:"version"`
	Notes      string `json:"notes,omitempty"`
	ReleaseURL string `json:"release_url"`
	APKURL     string `json:"apk_url,omitempty"`
	ExeURL     string `json:"exe_url,omitempty"`
}

// Checker is safe for concurrent use.
type Checker struct {
	client  *http.Client
	apiURL  string // overridable in tests
	pageURL string

	mu        sync.Mutex
	cached    *Info
	fetchedAt time.Time
	failedAt  time.Time
}

func NewChecker(client *http.Client) *Checker {
	return &Checker{
		client:  client,
		apiURL:  fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", repo),
		pageURL: fmt.Sprintf("https://github.com/%s/releases/latest", repo),
	}
}

// Latest returns info about the newest release, or nil when the lookup failed,
// timed out, or a cached result is stale with a recent failure behind it.
func (c *Checker) Latest(ctx context.Context) *Info {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	if c.cached != nil && now.Sub(c.fetchedAt) < successTTL {
		return c.cached
	}
	if !c.failedAt.IsZero() && now.Sub(c.failedAt) < failureTTL {
		return nil
	}

	info, err := c.lookup(ctx)
	if err != nil {
		c.failedAt = now
		return nil
	}
	c.cached = info
	c.fetchedAt = now
	c.failedAt = time.Time{}
	return info
}

func (c *Checker) lookup(ctx context.Context) (*Info, error) {
	if info, err := c.viaAPI(ctx); err == nil {
		return info, nil
	}
	return c.viaRedirect(ctx)
}

// viaAPI reads the releases/latest JSON endpoint and picks the two fixed
// download assets out of the asset list.
func (c *Checker) viaAPI(ctx context.Context) (*Info, error) {
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("api status %d", resp.StatusCode)
	}
	var payload struct {
		TagName string `json:"tag_name"`
		Body    string `json:"body"`
		HTMLURL string `json:"html_url"`
		Assets  []struct {
			Name               string `json:"name"`
			BrowserDownloadURL string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	info := &Info{Version: payload.TagName, Notes: strings.TrimSpace(payload.Body), ReleaseURL: payload.HTMLURL}
	for _, asset := range payload.Assets {
		switch {
		case asset.Name == exeAssetName:
			info.ExeURL = asset.BrowserDownloadURL
		// 历史发布的 APK 名不带 v 前缀（edh-powerlevel-20260922-1256.apk），
		// 新发布与 tag 一致（edh-powerlevel-v20260922-1030.apk）；每个 release
		// 只有一个 APK，按后缀匹配两者兼容。
		case strings.HasSuffix(asset.Name, ".apk"):
			info.APKURL = asset.BrowserDownloadURL
		}
	}
	if !valid(info) {
		return nil, errors.New("api response missing tag or release URL")
	}
	return info, nil
}

// viaRedirect resolves the human-facing releases/latest page, which 302s to
// releases/tag/<tag>. Only the tag is available this way — notes and asset
// URLs are reconstructed from the fixed release layout.
func (c *Checker) viaRedirect(ctx context.Context) (*Info, error) {
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, c.pageURL, nil)
	if err != nil {
		return nil, err
	}
	client := *c.client
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse // want the 302 itself, not the target
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil || loc.Path == "" {
		return nil, errors.New("redirect missing Location")
	}
	parts := strings.Split(loc.Path, "/")
	tag := parts[len(parts)-1]
	tagVersionRe := regexp.MustCompile(`^v\d{8}-\d{4}$`)
	if !tagVersionRe.MatchString(tag) {
		return nil, fmt.Errorf("unexpected tag %q", tag)
	}
	base := fmt.Sprintf("https://github.com/%s/releases", repo)
	return &Info{
		Version:    tag,
		ReleaseURL: base + "/tag/" + tag,
		APKURL:     base + "/download/" + tag + "/" + fmt.Sprintf(apkAssetFmt, tag),
		ExeURL:     base + "/download/" + tag + "/" + exeAssetName,
	}, nil
}

func valid(info *Info) bool {
	return info.Version != "" && info.ReleaseURL != ""
}
