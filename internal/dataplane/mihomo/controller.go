package mihomo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

var ErrNotRunning = errors.New("mihomo is not running")

type DelayResult struct {
	Delay int `json:"delay"`
}

func (m *Manager) TestProxy(
	ctx context.Context,
	fingerprint string,
	testURL string,
	timeout time.Duration,
) (int, error) {
	fingerprint = strings.TrimSpace(fingerprint)
	if fingerprint == "" {
		return 0, errors.New("node fingerprint is required")
	}
	testURL = strings.TrimSpace(testURL)
	parsedURL, err := url.Parse(testURL)
	if err != nil || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") || parsedURL.Host == "" {
		return 0, errors.New("test URL must use HTTP or HTTPS")
	}
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	if timeout > 30*time.Second {
		timeout = 30 * time.Second
	}

	m.mu.Lock()
	m.refreshProcessLocked()
	if m.process == nil || !m.status.Running {
		m.mu.Unlock()
		return 0, ErrNotRunning
	}
	socketPath := m.controllerSocket
	m.mu.Unlock()

	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(dialContext context.Context, _, _ string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(dialContext, "unix", socketPath)
		},
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: timeout + time.Second}
	proxyName := nodeProxyName(fingerprint)
	requestURL := "http://unix/proxies/" + url.PathEscape(proxyName) + "/delay?" + url.Values{
		"timeout":  {strconv.FormatInt(timeout.Milliseconds(), 10)},
		"url":      {testURL},
		"expected": {"200-299"},
	}.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return 0, fmt.Errorf("create Mihomo delay request: %w", err)
	}
	response, err := client.Do(request)
	if err != nil {
		return 0, fmt.Errorf("Mihomo proxy delay request failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 1024))
		var payload struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(body, &payload)
		message := strings.TrimSpace(payload.Message)
		if message == "" {
			message = strings.TrimSpace(string(body))
		}
		if len(message) > 256 {
			message = message[:256]
		}
		if message == "" {
			return 0, fmt.Errorf("Mihomo proxy delay returned status %d", response.StatusCode)
		}
		return 0, fmt.Errorf("Mihomo proxy delay returned status %d: %s", response.StatusCode, message)
	}
	var result DelayResult
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		return 0, fmt.Errorf("decode Mihomo proxy delay: %w", err)
	}
	if result.Delay <= 0 {
		return 0, errors.New("Mihomo returned an invalid proxy delay")
	}
	return result.Delay, nil
}
