package residential

import (
	"fmt"
	"net"
	"strings"

	"github.com/HengXin666/HX-ProxyGroup/internal/listener"
)

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
