package config

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Address             string
	RequestTimeout      time.Duration
	ProviderTimeout     time.Duration
	CacheTTL            time.Duration
	PartialCacheTTL     time.Duration
	CacheMaxEntries     int
	ScryfallAPIURL      string
	ScryfallImageURL    string
	CardCatalogTTL      time.Duration
	SpellbookAPIURL     string
	EDHRECJSONURL       string
	MoxfieldAPIURL      string
	CommanderSaltAPIURL string
	// CacheDir is the on-disk home for the persistent caches (card catalog,
	// proxied card images). It is what makes restarts warm and lets the app
	// ride out upstream outages with slightly-old data.
	CacheDir string
	// UpstreamProxy optionally routes every upstream request (Scryfall, EDHREC,
	// Moxfield, …) through an HTTP proxy, e.g. http://127.0.0.1:7890. Empty
	// keeps the default behavior of honoring the standard HTTP(S)_PROXY env vars.
	UpstreamProxy string
}

func Load() Config {
	return Config{
		Address:             env("APP_ADDRESS", ":18781"),
		RequestTimeout:      durationEnv("REQUEST_TIMEOUT", 90*time.Second),
		ProviderTimeout:     durationEnv("PROVIDER_TIMEOUT", 60*time.Second),
		CacheTTL:            durationEnv("CACHE_TTL", 30*time.Minute),
		PartialCacheTTL:     durationEnv("PARTIAL_CACHE_TTL", 45*time.Second),
		CacheMaxEntries:     intEnv("CACHE_MAX_ENTRIES", 500, 1),
		ScryfallAPIURL:      env("SCRYFALL_API_URL", "https://api.scryfall.com"),
		ScryfallImageURL:    env("SCRYFALL_IMAGE_URL", "https://cards.scryfall.io"),
		CardCatalogTTL:      durationEnv("CARD_CATALOG_TTL", 24*time.Hour),
		SpellbookAPIURL:     env("SPELLBOOK_API_URL", "https://backend.commanderspellbook.com"),
		EDHRECJSONURL:       env("EDHREC_JSON_URL", "https://json.edhrec.com"),
		MoxfieldAPIURL:      env("MOXFIELD_API_URL", "https://api2.moxfield.com"),
		CommanderSaltAPIURL: env("COMMANDERSALT_API_URL", "https://api.commandersalt.com"),
		CacheDir:            env("CACHE_DIR", defaultCacheDir()),
		UpstreamProxy:       env("UPSTREAM_PROXY", ""),
	}
}

// defaultCacheDir prefers the OS per-user cache directory; the TempDir fallback
// covers environments without one (notably the gomobile Android shell, where the
// generated binding exposes the app cache dir via TMPDIR).
func defaultCacheDir() string {
	if base, err := os.UserCacheDir(); err == nil {
		return filepath.Join(base, "powerlevel")
	}
	return filepath.Join(os.TempDir(), "powerlevel-cache")
}

// ProxyFunc resolves the proxy for outbound provider requests: UPSTREAM_PROXY
// when set to a usable http(s) URL, otherwise the standard HTTP(S)_PROXY
// environment behavior. Customers behind firewalls or local accelerators can
// point every provider at one proxy without touching their shell env.
func (c Config) ProxyFunc() func(*http.Request) (*url.URL, error) {
	if value := strings.TrimSpace(c.UpstreamProxy); value != "" {
		if parsed, err := url.Parse(value); err == nil && parsed.Scheme != "" && parsed.Host != "" {
			return func(*http.Request) (*url.URL, error) { return parsed, nil }
		}
	}
	return http.ProxyFromEnvironment
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func intEnv(key string, fallback, minimum int) int {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < minimum {
		return fallback
	}
	return parsed
}

func boolEnv(key string, fallback bool) bool {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func durationEnv(key string, fallback time.Duration) time.Duration {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	if duration, err := time.ParseDuration(value); err == nil {
		return duration
	}
	if seconds, err := strconv.Atoi(value); err == nil {
		return time.Duration(seconds) * time.Second
	}
	return fallback
}
