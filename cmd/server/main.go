package main

import (
	"context"
	"embed"
	"errors"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
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
var embeddedWeb embed.FS

func main() {
	cfg := config.Load()
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	httpClient := &http.Client{
		Transport: &http.Transport{
			Proxy:       http.ProxyFromEnvironment,
			DialContext: (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			// Disable HTTP/2 to avoid TLS renegotiation issues with some CDNs (e.g., CloudFront/EDHREC)
			ForceAttemptHTTP2:     false,
			MaxIdleConns:          20,
			MaxIdleConnsPerHost:   10,
			MaxConnsPerHost:       10,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: cfg.ProviderTimeout,
		},
		Timeout: cfg.ProviderTimeout,
		// Allow redirects for all providers - EDHREC may redirect to S3
	}
	// Create a separate client for Moxfield that blocks redirects
	moxfieldHTTPClient := &http.Client{
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			ForceAttemptHTTP2:     false,
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
	moxfieldClient := moxfield.New(cfg.MoxfieldAPIURL, moxfieldHTTPClient)
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

	webRoot, err := fs.Sub(embeddedWeb, "web")
	if err != nil {
		logger.Error("load embedded web files", "error", err)
		os.Exit(1)
	}
	server := &http.Server{
		Addr:              cfg.Address,
		Handler:           api.NewHandler(analyzer, logger, cfg.RequestTimeout, http.FileServer(http.FS(webRoot))),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      cfg.RequestTimeout + 5*time.Second,
		IdleTimeout:       60 * time.Second,
	}
	logger.Info("server starting", "address", cfg.Address)
	serverErrors := make(chan error, 1)
	go func() {
		serverErrors <- server.ListenAndServe()
	}()

	// 双击 server.exe 或绕过 start.ps1 直接运行时，也主动打开浏览器。
	// 仅在用户明确关闭时才跳过，避免后续版本意外删除该行为。
	if os.Getenv("POWERLEVEL_OPEN_BROWSER") != "0" {
		go openBrowser(cfg.Address, logger)
	}

	signalCtx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	select {
	case <-signalCtx.Done():
		logger.Info("server shutting down")
		shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancelShutdown()
		if err := server.Shutdown(shutdownCtx); err != nil {
			logger.Error("graceful shutdown failed", "error", err)
			_ = server.Close()
		}
	case err := <-serverErrors:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server stopped", "error", err)
			os.Exit(1)
		}
	}
}

// openBrowser 等待服务就绪后，用默认浏览器打开 web 界面。
// Windows 依靠正确的浏览器路径；其他平台通过默认 handler 打开。
func openBrowser(addr string, logger *slog.Logger) {
	url := browserURL(addr)
	if url == "" {
		return
	}
	healthURL := url + "healthz"
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(healthURL)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				openInBrowser(url, logger)
				return
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	logger.Warn("server did not become ready in time; skipping browser open", "url", url)
}

// browserURL 把监听地址折算成可访问的 http URL，忽略动态端口或空地址。
func browserURL(addr string) string {
	addr = strings.TrimSpace(addr)
	switch {
	case addr == "" || addr == ":0":
		return ""
	case strings.HasPrefix(addr, ":"), strings.HasPrefix(addr, "0.0.0.0"), strings.HasPrefix(addr, "[::]"):
		return "http://127.0.0.1" + addr + "/"
	default:
		return "http://" + addr + "/"
	}
}

func openInBrowser(url string, logger *slog.Logger) {
	if browserPath := os.Getenv("BROWSER_PATH"); browserPath != "" {
		if err := exec.Command(browserPath, url).Start(); err == nil {
			return
		}
	}
	var err error
	switch runtime.GOOS {
	case "windows":
		err = exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	case "darwin":
		err = exec.Command("open", url).Start()
	default:
		err = exec.Command("xdg-open", url).Start()
	}
	if err != nil {
		logger.Warn("failed to open browser", "error", err, "url", url)
	}
}
