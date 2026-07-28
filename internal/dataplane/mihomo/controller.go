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

	"github.com/HengXin666/HX-ProxyGroup/internal/metrics"
	"github.com/HengXin666/HX-ProxyGroup/internal/store"
)

var ErrNotRunning = errors.New("mihomo is not running")

type DelayResult struct {
	Delay int `json:"delay"`
}

type connectionsResponse struct {
	Connections []runtimeConnection `json:"connections"`
}

type runtimeConnection struct {
	ID       string             `json:"id"`
	Upload   int64              `json:"upload"`
	Download int64              `json:"download"`
	Chains   []string           `json:"chains"`
	Metadata connectionMetadata `json:"metadata"`
}

type connectionMetadata struct {
	InboundName  string `json:"inboundName"`
	SpecialProxy string `json:"specialProxy"`
}

func (m *Manager) TrafficSnapshot(ctx context.Context) (metrics.RuntimeSnapshot, error) {
	m.mu.Lock()
	m.refreshProcessLocked()
	if m.process == nil || !m.status.Running {
		m.mu.Unlock()
		return metrics.RuntimeSnapshot{}, ErrNotRunning
	}
	socketPath := m.controllerSocket
	endpoints := append([]Endpoint(nil), m.lastCompiled.Endpoints...)
	m.mu.Unlock()

	nodes, err := m.compiler.repository.ListNodeConfigs(ctx, nil)
	if err != nil {
		return metrics.RuntimeSnapshot{}, fmt.Errorf("list traffic node identities: %w", err)
	}
	groups, err := m.compiler.repository.ListProxyGroups(ctx)
	if err != nil {
		return metrics.RuntimeSnapshot{}, fmt.Errorf("list traffic group identities: %w", err)
	}
	identities := make(map[string]metrics.Resource, len(nodes)+len(groups)+len(endpoints))
	for _, record := range nodes {
		identities[nodeProxyName(record.Fingerprint)] = metrics.Resource{Type: store.TrafficResourceNode, ID: record.ID}
	}
	for _, record := range groups {
		identities[record.Name] = metrics.Resource{Type: store.TrafficResourceProxyGroup, ID: record.ID}
	}
	for _, endpoint := range endpoints {
		identities[listenerConfigName(endpoint.ID)] = metrics.Resource{Type: store.TrafficResourceListener, ID: endpoint.ID}
	}

	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(dialContext context.Context, _, _ string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(dialContext, "unix", socketPath)
		},
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 4 * time.Second}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://unix/connections", nil)
	if err != nil {
		return metrics.RuntimeSnapshot{}, fmt.Errorf("create Mihomo connections request: %w", err)
	}
	response, err := client.Do(request)
	if err != nil {
		if strings.Contains(err.Error(), "dial unix") {
			return metrics.RuntimeSnapshot{}, fmt.Errorf("%w: control socket unreachable", ErrNotRunning)
		}
		return metrics.RuntimeSnapshot{}, fmt.Errorf("request Mihomo connections: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return metrics.RuntimeSnapshot{}, fmt.Errorf("Mihomo connections returned status %d", response.StatusCode)
	}
	var payload connectionsResponse
	decoder := json.NewDecoder(io.LimitReader(response.Body, 16<<20))
	if err := decoder.Decode(&payload); err != nil {
		return metrics.RuntimeSnapshot{}, fmt.Errorf("decode Mihomo connections: %w", err)
	}
	return mapTrafficConnections(payload.Connections, identities), nil
}

func mapTrafficConnections(connections []runtimeConnection, identities map[string]metrics.Resource) metrics.RuntimeSnapshot {
	result := metrics.RuntimeSnapshot{Connections: make([]metrics.Connection, 0, len(connections))}
	for _, connection := range connections {
		resources := make([]metrics.Resource, 0, len(connection.Chains)+2)
		if resource, exists := identities[connection.Metadata.InboundName]; exists {
			resources = append(resources, resource)
		}
		if resource, exists := identities[connection.Metadata.SpecialProxy]; exists {
			resources = append(resources, resource)
		}
		for _, name := range connection.Chains {
			if resource, exists := identities[name]; exists {
				resources = append(resources, resource)
			}
		}
		result.Connections = append(result.Connections, metrics.Connection{
			ID: connection.ID, Upload: connection.Upload, Download: connection.Download, Resources: resources,
		})
	}
	return result
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
		if strings.Contains(err.Error(), "dial unix") {
			return 0, fmt.Errorf("%w: control socket unreachable", ErrNotRunning)
		}
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
