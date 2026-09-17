package cardcatalog

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	maxAttempts    = 3
	retryBaseDelay = 500 * time.Millisecond
	maxRetryDelay  = 5 * time.Second
)

// doWithRetry performs an upstream request with up to maxAttempts attempts,
// re-invoking buildReq each time so POST bodies are re-created. Transient
// transport failures and retryable status codes back off between attempts;
// a 429 honors Scryfall's Retry-After (capped so one bad response cannot
// stall a whole analysis). The handler-scoped context bounds the total time:
// once its deadline passes the retry loop gives up immediately.
func (c *Client) doWithRetry(ctx context.Context, buildReq func() (*http.Request, error)) (*http.Response, error) {
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			delay := retryBaseDelay * time.Duration(attempt)
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, ctx.Err()
			case <-timer.C:
			}
		}
		req, err := buildReq()
		if err != nil {
			return nil, err
		}
		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		if !retryableStatus(resp.StatusCode) {
			return resp, nil
		}
		lastErr = fmt.Errorf("Scryfall returned HTTP %d", resp.StatusCode)
		delay := retryDelay(resp.Header.Get("Retry-After"), attempt)
		drainAndClose(resp)
		if attempt == maxAttempts-1 {
			return nil, lastErr
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	return nil, lastErr
}

// retryableStatus covers transport-level hiccups Scryfall's CDN reports as 5xx
// plus rate limiting; 4xx client errors (404 etc.) are answers, not failures.
func retryableStatus(status int) bool {
	switch status {
	case http.StatusTooManyRequests, http.StatusInternalServerError,
		http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
}

func retryDelay(retryAfter string, attempt int) time.Duration {
	if seconds, err := strconv.Atoi(strings.TrimSpace(retryAfter)); err == nil && seconds > 0 {
		if delay := time.Duration(seconds) * time.Second; delay < maxRetryDelay {
			return delay
		}
		return maxRetryDelay
	}
	delay := retryBaseDelay * time.Duration(attempt+1)
	if delay > maxRetryDelay {
		return maxRetryDelay
	}
	return delay
}

func drainAndClose(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	_ = resp.Body.Close()
}
