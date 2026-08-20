// Package mobile is the gomobile bind entry point for the Android APK.
//
// It exposes a single generated-Java API (App.Start(port) -> string) that spins up the
// full analysis server in-process over loopback, returning the base URL the WebView
// shell should load. The Go server itself is identical to cmd/server but is wired to
// listen on 127.0.0.1:<port> (a free port supplied by the Android shell via
// TcpListener) and skips the browser-open / signal-handling logic that only makes sense
// on a host OS.
package mobile

import (
	"embed"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	"powerlevel/internal/api"
	"powerlevel/internal/config"
	"powerlevel/internal/providers/cardcatalog"
	"powerlevel/internal/providers/commandersalt"
	"powerlevel/internal/providers/edhrec"
	"powerlevel/internal/providers/moxfield"
	"powerlevel/internal/providers/spellbook"
	"powerlevel/internal/service"
)

//go:embed web/*
var embedWebDir embed.FS

// Start boots the analysis HTTP server on 127.0.0.1:<port> and returns the base URL
// (e.g. "http://127.0.0.1:39123/") for the Android WebView to load. It must be called
// once from the app shell before the WebView navigates. An empty string means startup
// failed; the (best-effort) reason is logged to logcat via the returned error text.
func Start(port int) string {
	if port <= 0 || port > 65535 {
		port = 0 // let the kernel choose on "any" request
	}
	cfg := config.Load()
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	httpClient := &http.Client{
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          20,
			MaxIdleConnsPerHost:   10,
			MaxConnsPerHost:       10,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: cfg.ProviderTimeout,
		},
		Timeout: cfg.ProviderTimeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	commanderSaltClient := commandersalt.New(cfg.CommanderSaltAPIURL, httpClient)
	moxfieldClient := moxfield.New(cfg.MoxfieldAPIURL, httpClient)
	cardCatalogClient := cardcatalog.New(cfg.ScryfallAPIURL, httpClient, cfg.CardCatalogTTL)
	spellbookClient := spellbook.New(cfg.SpellbookAPIURL, httpClient)
	edhrecClient := edhrec.New(cfg.EDHRECJSONURL, httpClient)
	analyzer := service.NewAnalyzer(
		moxfieldClient,
		commanderSaltClient,
		nil, // EDH Power Level is scored in-process via getcards (no browser client).
		httpClient,
		cardCatalogClient,
		spellbookClient,
		edhrecClient,
		cfg.ProviderTimeout,
		cfg.RequestTimeout,
		cfg.CacheTTL,
		cfg.PartialCacheTTL,
		cfg.CacheMaxEntries,
	)

	webRoot, err := fs.Sub(embedWebDir, "web")
	if err != nil {
		logger.Error("load embedded web files", "error", err)
		return ""
	}

	// Bind to an explicit loopback address so the WebView can reach the server in-process.
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		// Fall back to kernel-assigned port (covers "0" and transient conflicts).
		listener, err = net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			logger.Error("bind loopback listener", "error", err)
			return ""
		}
	}
	realPort := listener.Addr().(*net.TCPAddr).Port

	server := &http.Server{
		Handler:           api.NewHandler(analyzer, logger, cfg.RequestTimeout, http.FileServer(http.FS(webRoot))),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      cfg.RequestTimeout + 5*time.Second,
		IdleTimeout:       60 * time.Second,
	}
	logger.Info("mobile server starting", "address", listener.Addr().String())
	go func() {
		if serveErr := server.Serve(listener); serveErr != nil && serveErr != http.ErrServerClosed {
			logger.Error("mobile server stopped", "error", serveErr)
		}
	}()

	return fmt.Sprintf("http://127.0.0.1:%d/", realPort)
}
