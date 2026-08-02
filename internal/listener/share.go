package listener

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// ErrShareDisabled marks share exports rejected because the listener is
// disabled; the API maps it onto 404 to avoid leaking listener existence.
var ErrShareDisabled = errors.New("listener share link is disabled")

// ShareExport is the rendered subscription payload for one listener.
type ShareExport struct {
	// Body is the plain URI list, one proxy URI per line.
	Body string
	// FileName is a suggested download name.
	FileName  string
	Name      string
	Kind      string
	Host      string
	Port      int
	Auth      *Auth
	Transport Transport
	Endpoint  PublicEndpoint
}

func newShareToken() (string, error) {
	var buffer [16]byte
	if _, err := rand.Read(buffer[:]); err != nil {
		return "", fmt.Errorf("generate listener share token: %w", err)
	}
	return hex.EncodeToString(buffer[:]), nil
}

// ExportByShareToken renders the subscription body for the enabled listener
// owning the token. requestHost is the host (optionally host:port) the client
// used to reach the control plane. An explicit public endpoint takes priority
// over the request host when the data-plane listener is bound to loopback.
func (s *Service) ExportByShareToken(ctx context.Context, token, requestHost string) (ShareExport, error) {
	token = strings.TrimSpace(token)
	if len(token) < 16 || len(token) > 64 {
		return ShareExport{}, ErrNotFound
	}
	record, err := s.repository.GetListenerByShareToken(ctx, token)
	if err != nil {
		return ShareExport{}, mapStoreError(err)
	}
	if !record.Enabled {
		return ShareExport{}, ErrShareDisabled
	}
	var auth *Auth
	if record.AuthMode == "userpass" && len(record.AuthConfigEncrypted) > 0 {
		plaintext, err := s.cipher.Open(record.AuthConfigEncrypted, associatedData(record.ID))
		if err != nil {
			return ShareExport{}, fmt.Errorf("decrypt listener %q auth: %w", record.Name, err)
		}
		decoded := Auth{}
		if err := json.Unmarshal(plaintext, &decoded); err != nil {
			return ShareExport{}, fmt.Errorf("decode listener %q auth: %w", record.Name, err)
		}
		auth = &decoded
	}
	var transport Transport
	var endpoint PublicEndpoint
	_ = json.Unmarshal([]byte(record.TransportJSON), &transport)
	_ = json.Unmarshal([]byte(record.PublicEndpointJSON), &endpoint)
	if isAdvancedKind(record.Kind) {
		normalizedPath, err := NormalizeWebSocketPath(transport.WSPath)
		if err != nil {
			return ShareExport{}, err
		}
		transport.WSPath = normalizedPath
	}
	host := exportHost(record.BindAddress, requestHost)
	port := record.Port
	if endpoint.Host == "" && isLoopbackBind(record.BindAddress) {
		// Do not turn an internal Mihomo port into an externally advertised
		// subscription just because a reverse proxy forwarded the request.
		return ShareExport{}, ErrShareDisabled
	}
	if endpoint.Host != "" {
		host = endpoint.Host
		if endpoint.Port > 0 {
			port = endpoint.Port
		}
	}
	if isAdvancedKind(record.Kind) {
		host = endpoint.Host
		port = endpoint.Port
	}
	if port < 1 || port > 65535 {
		return ShareExport{}, fmt.Errorf("%w: listener export port must be between 1 and 65535", ErrInvalid)
	}
	uris := shareURIs(record.Kind, record.Name, host, port, auth, transport, endpoint)
	return ShareExport{
		Body:     strings.Join(uris, "\n") + "\n",
		FileName: sanitizeFileName(record.Name) + ".txt",
		Name:     record.Name, Kind: record.Kind, Host: host, Port: port,
		Auth: auth, Transport: transport, Endpoint: endpoint,
	}, nil
}

func isLoopbackBind(value string) bool {
	ip := net.ParseIP(strings.TrimSpace(value))
	return ip != nil && ip.IsLoopback()
}

// EncodeSubscription renders the conventional base64 subscription body used
// by most proxy clients.
func (export ShareExport) EncodeSubscription() string {
	return base64.StdEncoding.EncodeToString([]byte(export.Body))
}

// Render returns a client-specific subscription document. v2rayn remains the
// default because it accepts the conventional base64-wrapped URI list.
func (export ShareExport) Render(format string) (body, fileName, contentType string, err error) {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "", "v2rayn":
		return export.EncodeSubscription(), sanitizeFileName(export.Name) + ".txt", "text/plain; charset=utf-8", nil
	case "uri":
		return export.Body, sanitizeFileName(export.Name) + ".txt", "text/plain; charset=utf-8", nil
	case "clash", "mihomo":
		groupName := "HX-PROXY"
		if export.Name == groupName {
			groupName = "HX-PROXY-GROUP"
		}
		encoded, encodeErr := yaml.Marshal(map[string]any{
			"mode":      "rule",
			"log-level": "info",
			"allow-lan": false,
			"proxies":   []map[string]any{export.clashProxy()},
			"proxy-groups": []map[string]any{{
				"name": groupName, "type": "select", "proxies": []string{export.Name, "DIRECT"},
			}},
			"rules": []string{"MATCH," + groupName},
		})
		if encodeErr != nil {
			return "", "", "", fmt.Errorf("encode Clash subscription: %w", encodeErr)
		}
		return string(encoded), sanitizeFileName(export.Name) + ".yaml", "application/yaml; charset=utf-8", nil
	case "sing-box", "singbox":
		encoded, encodeErr := json.MarshalIndent(map[string]any{"outbounds": []map[string]any{export.singBoxOutbound()}}, "", "  ")
		if encodeErr != nil {
			return "", "", "", fmt.Errorf("encode sing-box subscription: %w", encodeErr)
		}
		return string(encoded) + "\n", sanitizeFileName(export.Name) + ".json", "application/json; charset=utf-8", nil
	default:
		return "", "", "", fmt.Errorf("%w: format must be v2rayn, clash, sing-box, or uri", ErrInvalid)
	}
}

func exportHost(bindAddress, requestHost string) string {
	ip := net.ParseIP(bindAddress)
	if ip != nil && !ip.IsLoopback() && !ip.IsUnspecified() {
		return bindAddress
	}
	if host, _, err := net.SplitHostPort(requestHost); err == nil && host != "" {
		return host
	}
	if requestHost != "" {
		return requestHost
	}
	return bindAddress
}

func shareURIs(kind, name, host string, port int, auth *Auth, transport Transport, endpoint PublicEndpoint) []string {
	if kind == "vmess" {
		payload := map[string]any{
			"v": "2", "ps": name, "add": host, "port": strconv.Itoa(port),
			"id": authPassword(auth), "aid": "0", "scy": "auto", "net": "ws",
			"type": "none", "host": endpoint.Host, "path": transport.WSPath, "tls": "tls", "sni": endpoint.Host,
		}
		encoded, _ := json.Marshal(payload)
		return []string{"vmess://" + base64.RawStdEncoding.EncodeToString(encoded)}
	}
	var schemes []string
	switch kind {
	case "http":
		if endpoint.TLS {
			schemes = []string{"https"}
		} else {
			schemes = []string{"http"}
		}
	case "socks":
		schemes = []string{"socks5"}
	case "vless", "trojan":
		schemes = []string{kind}
	default: // mixed exposes both entry protocols on one port
		httpScheme := "http"
		if endpoint.TLS {
			httpScheme = "https"
		}
		schemes = []string{httpScheme, "socks5"}
	}
	uris := make([]string, 0, len(schemes))
	for _, scheme := range schemes {
		address := url.URL{
			Scheme:   scheme,
			Host:     net.JoinHostPort(host, strconv.Itoa(port)),
			Fragment: name,
		}
		if auth != nil {
			if kind == "vless" || kind == "trojan" {
				address.User = url.User(auth.Password)
			} else {
				address.User = url.UserPassword(auth.Username, auth.Password)
			}
		}
		if kind == "vless" || kind == "trojan" {
			query := url.Values{"security": {"tls"}, "type": {"ws"}, "host": {endpoint.Host}, "path": {transport.WSPath}, "sni": {endpoint.Host}}
			if kind == "vless" {
				query.Set("encryption", "none")
			}
			address.RawQuery = query.Encode()
		}
		uris = append(uris, address.String())
	}
	return uris
}

func (export ShareExport) clashProxy() map[string]any {
	proxy := map[string]any{"name": export.Name, "type": export.Kind, "server": export.Host, "port": export.Port}
	switch export.Kind {
	case "http", "socks":
		if export.Kind == "http" && export.Endpoint.TLS {
			proxy["tls"] = true
		}
		if export.Kind == "socks" {
			proxy["type"] = "socks5"
		}
		addUserPassword(proxy, export.Auth)
	case "mixed":
		proxy["type"] = "http"
		if export.Endpoint.TLS {
			proxy["tls"] = true
		}
		addUserPassword(proxy, export.Auth)
	case "vless", "vmess", "trojan":
		proxy["tls"] = true
		proxy["network"] = "ws"
		proxy["server-name"] = export.Endpoint.Host
		proxy["ws-opts"] = map[string]any{"path": export.Transport.WSPath, "headers": map[string]string{"Host": export.Endpoint.Host}}
		if export.Kind == "trojan" {
			proxy["password"] = authPassword(export.Auth)
		} else {
			proxy["uuid"] = authPassword(export.Auth)
		}
		if export.Kind == "vless" {
			proxy["udp"] = true
		}
	}
	return proxy
}

func (export ShareExport) singBoxOutbound() map[string]any {
	outbound := map[string]any{"type": export.Kind, "tag": export.Name, "server": export.Host, "server_port": export.Port}
	switch export.Kind {
	case "http", "socks", "mixed":
		if export.Kind == "mixed" {
			outbound["type"] = "http"
		}
		if export.Kind == "socks" {
			outbound["type"] = "socks"
		}
		addUserPassword(outbound, export.Auth)
	case "vless", "vmess", "trojan":
		outbound["tls"] = map[string]any{"enabled": true, "server_name": export.Endpoint.Host}
		outbound["transport"] = map[string]any{"type": "ws", "path": export.Transport.WSPath, "headers": map[string]string{"Host": export.Endpoint.Host}}
		if export.Kind == "trojan" {
			outbound["password"] = authPassword(export.Auth)
		} else {
			outbound["uuid"] = authPassword(export.Auth)
		}
		if export.Kind == "vmess" {
			outbound["security"] = "auto"
			outbound["alter_id"] = 0
		}
	}
	return outbound
}

func addUserPassword(target map[string]any, auth *Auth) {
	if auth == nil {
		return
	}
	target["username"] = auth.Username
	target["password"] = auth.Password
}

func authPassword(auth *Auth) string {
	if auth == nil {
		return ""
	}
	return auth.Password
}

func sanitizeFileName(name string) string {
	var builder strings.Builder
	for _, character := range name {
		switch {
		case character >= 'a' && character <= 'z',
			character >= 'A' && character <= 'Z',
			character >= '0' && character <= '9',
			character == '-', character == '_':
			builder.WriteRune(character)
		default:
			builder.WriteRune('_')
		}
	}
	if builder.Len() == 0 {
		return "listener"
	}
	return builder.String()
}

// RotateShareToken invalidates the current share link and returns the
// listener carrying the replacement token.
func (s *Service) RotateShareToken(ctx context.Context, id string) (Listener, error) {
	token, err := newShareToken()
	if err != nil {
		return Listener{}, err
	}
	record, err := s.repository.RotateListenerShareToken(ctx, id, token)
	if err != nil {
		return Listener{}, mapStoreError(err)
	}
	return fromRecord(record), nil
}
