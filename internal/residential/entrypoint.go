package residential

import (
	"fmt"
	"net"
	"strings"

	"github.com/HengXin666/HX-ProxyGroup/internal/listener"
)

// residentialBindAddress constrains the reverse-proxied WebSocket entry point
// to loopback. That listener is an internal implementation detail reached only
// through the control plane's edge relay.
func residentialBindAddress(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "127.0.0.1", nil
	}
	ip := net.ParseIP(value)
	if ip == nil || !ip.IsLoopback() {
		return "", fmt.Errorf("%w: residential listeners must bind to a loopback address", ErrInvalid)
	}
	return ip.String(), nil
}

// directEntryKinds are the protocols a directly reachable residential entry
// point may use. HTTP CONNECT and SOCKS5 cannot be carried by a layer-7 HTTPS
// reverse proxy, so they are only usable on a real TCP port.
func isDirectEntryKind(kind string) bool {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "http", "socks", "mixed":
		return true
	default:
		return false
	}
}

// validateDirectListenerRequest checks the optional public TCP entry point.
// This listener deliberately bypasses Cloudflare and the WAF, so it must bind
// a routable address and must always authenticate its callers.
func validateDirectListenerRequest(request *ChannelListenerRequest) error {
	if request == nil {
		return nil
	}
	if !isDirectEntryKind(request.Kind) {
		return fmt.Errorf(
			"%w: direct_listener.kind must be http, socks, or mixed; WebSocket protocols use the reverse-proxied entry point",
			ErrInvalid,
		)
	}
	address := strings.TrimSpace(request.BindAddress)
	if address == "" {
		return fmt.Errorf("%w: direct_listener.bind_address is required", ErrInvalid)
	}
	ip := net.ParseIP(address)
	if ip == nil {
		return fmt.Errorf("%w: direct_listener.bind_address must be an explicit IP", ErrInvalid)
	}
	if ip.IsLoopback() {
		return fmt.Errorf(
			"%w: direct_listener.bind_address must be reachable; use the WebSocket entry point for loopback-only setups",
			ErrInvalid,
		)
	}
	if request.Port < 1 || request.Port > 65535 {
		return fmt.Errorf("%w: direct_listener.port must be between 1 and 65535", ErrInvalid)
	}
	// A public port without authentication would be an open proxy the moment it
	// is published, so this is rejected rather than defaulted.
	if request.Auth == nil ||
		strings.TrimSpace(request.Auth.Username) == "" ||
		request.Auth.Password == "" {
		return fmt.Errorf(
			"%w: direct_listener requires a username and password because it bypasses the reverse proxy",
			ErrInvalid,
		)
	}
	return nil
}

func isResidentialWebSocketKind(kind string) bool {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "vless", "vmess", "trojan":
		return true
	default:
		return false
	}
}

func validateResidentialWebSocketEndpoint(endpoint listener.PublicEndpoint) error {
	if strings.TrimSpace(endpoint.Host) == "" {
		return nil
	}
	if endpoint.Port != 0 && endpoint.Port != 443 {
		return fmt.Errorf("%w: residential WebSocket public endpoint must use HTTPS port 443", ErrInvalid)
	}
	if !endpoint.TLS {
		return fmt.Errorf("%w: residential WebSocket public endpoint must use TLS", ErrInvalid)
	}
	return nil
}
