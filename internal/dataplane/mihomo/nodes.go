package mihomo

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/HengXin666/HX-ProxyGroup/internal/store"
)

func (c *Compiler) decryptNode(record store.NodeConfigRecord, proxyName string) (map[string]any, error) {
	plaintext, err := c.cipher.Open(
		record.CanonicalConfigEncrypted,
		[]byte("node:"+record.Fingerprint),
	)
	if err != nil {
		return nil, fmt.Errorf("decrypt node %q: %w", record.DisplayName, err)
	}
	var canonical map[string]any
	if err := json.Unmarshal(plaintext, &canonical); err != nil {
		return nil, fmt.Errorf("decode node %q canonical config: %w", record.DisplayName, err)
	}
	config, err := convertNodeConfig(canonical)
	if err != nil {
		return nil, fmt.Errorf("convert node %q: %w", record.DisplayName, err)
	}
	config["name"] = proxyName
	return config, nil
}

func convertNodeConfig(canonical map[string]any) (map[string]any, error) {
	config := cloneMap(canonical)
	delete(config, "name")
	protocol := strings.ToLower(stringValue(config["type"]))
	if protocol == "" {
		return nil, fmt.Errorf("node type is empty")
	}
	switch protocol {
	case "https":
		config["type"] = "http"
		config["tls"] = true
	case "socks":
		config["type"] = "socks5"
	case "anytls", "hysteria", "hysteria2", "http", "mieru", "shadow-tls", "snell", "socks5", "ss", "ssh", "ssr", "trojan", "tuic", "vless", "wireguard":
		config["type"] = protocol
	case "vmess":
		config["type"] = "vmess"
		moveKey(config, "id", "uuid")
		moveKey(config, "scy", "cipher")
		moveKey(config, "net", "network")
		if value, exists := config["aid"]; exists {
			if number, ok := integer(value); ok {
				config["alterId"] = number
			}
			delete(config, "aid")
		}
		delete(config, "v")
	default:
		return nil, fmt.Errorf("unsupported node type %q", protocol)
	}
	if value, exists := config["port"]; exists {
		if number, ok := integer(value); ok {
			config["port"] = number
		} else {
			return nil, fmt.Errorf("invalid port")
		}
	}
	applyTLS(config)
	applyTransport(config)
	applyQuery(config)
	delete(config, "ps")
	delete(config, "query")
	return config, nil
}

func applyTLS(config map[string]any) {
	value, exists := config["tls"]
	if !exists {
		return
	}
	switch typed := value.(type) {
	case string:
		value := strings.ToLower(strings.TrimSpace(typed))
		config["tls"] = value == "true" || value == "tls" || value == "reality"
	case bool:
	default:
		config["tls"] = false
	}
	if serverName := firstString(config, "sni", "servername"); serverName != "" {
		config["servername"] = serverName
	}
	delete(config, "sni")
}

func applyTransport(config map[string]any) {
	network := strings.ToLower(firstString(config, "network", "net"))
	if network != "" {
		config["network"] = network
	}
	host := stringValue(config["host"])
	path := stringValue(config["path"])
	switch network {
	case "ws":
		options := map[string]any{}
		if path != "" {
			options["path"] = path
		}
		if host != "" {
			options["headers"] = map[string]string{"Host": host}
		}
		if len(options) > 0 {
			config["ws-opts"] = options
		}
	case "grpc":
		serviceName := firstString(config, "serviceName", "service-name")
		if serviceName != "" {
			config["grpc-opts"] = map[string]any{"grpc-service-name": serviceName}
		}
	case "h2":
		options := map[string]any{}
		if path != "" {
			options["path"] = path
		}
		if host != "" {
			options["host"] = []string{host}
		}
		if len(options) > 0 {
			config["h2-opts"] = options
		}
	}
	delete(config, "net")
	delete(config, "host")
	delete(config, "path")
	delete(config, "serviceName")
	delete(config, "service-name")
}

func applyQuery(config map[string]any) {
	raw, ok := config["query"].(map[string]any)
	if !ok {
		return
	}
	security := strings.ToLower(stringValue(raw["security"]))
	if security == "tls" || security == "reality" {
		config["tls"] = true
	}
	if security == "reality" {
		reality := map[string]any{}
		if publicKey := firstString(raw, "pbk", "public-key"); publicKey != "" {
			reality["public-key"] = publicKey
		}
		if shortID := firstString(raw, "sid", "short-id"); shortID != "" {
			reality["short-id"] = shortID
		}
		if len(reality) > 0 {
			config["reality-opts"] = reality
		}
	}
	if serverName := firstString(raw, "sni", "servername"); serverName != "" {
		config["servername"] = serverName
	}
	if fingerprint := firstString(raw, "fp", "client-fingerprint"); fingerprint != "" {
		config["client-fingerprint"] = fingerprint
	}
	if flow := stringValue(raw["flow"]); flow != "" {
		config["flow"] = flow
	}
	for _, mapping := range []struct{ from, to string }{
		{"alpn", "alpn"}, {"auth", "auth-str"}, {"auth_str", "auth-str"},
		{"congestion_control", "congestion-controller"}, {"disable_mtu_discovery", "disable-mtu-discovery"},
		{"downmbps", "down"}, {"heartbeat", "heartbeat-interval"}, {"obfs", "obfs"},
		{"obfs-password", "obfs-password"}, {"obfs_password", "obfs-password"},
		{"password", "password"}, {"reduce_rtt", "fast-open"}, {"udp_relay_mode", "udp-relay-mode"},
		{"upmbps", "up"}, {"uuid", "uuid"},
	} {
		if value, exists := raw[mapping.from]; exists && stringValue(value) != "" {
			config[mapping.to] = value
		}
	}
	if insecure := strings.ToLower(firstString(raw, "insecure", "allowinsecure", "skip-cert-verify")); insecure == "1" || insecure == "true" {
		config["skip-cert-verify"] = true
	}
	network := strings.ToLower(firstString(raw, "type", "network"))
	if network != "" && network != "tcp" {
		config["network"] = network
	}
	host := stringValue(raw["host"])
	path := stringValue(raw["path"])
	switch network {
	case "ws":
		options := map[string]any{}
		if path != "" {
			options["path"] = path
		}
		if host != "" {
			options["headers"] = map[string]string{"Host": host}
		}
		if len(options) > 0 {
			config["ws-opts"] = options
		}
	case "grpc":
		if serviceName := firstString(raw, "serviceName", "service-name"); serviceName != "" {
			config["grpc-opts"] = map[string]any{"grpc-service-name": serviceName}
		}
	}
}

func cloneMap(input map[string]any) map[string]any {
	result := make(map[string]any, len(input))
	for key, value := range input {
		switch typed := value.(type) {
		case map[string]any:
			result[key] = cloneMap(typed)
		case []any:
			result[key] = append([]any(nil), typed...)
		default:
			result[key] = value
		}
	}
	return result
}

func moveKey(values map[string]any, from, to string) {
	if value, exists := values[from]; exists {
		values[to] = value
		delete(values, from)
	}
}

func firstString(values map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := stringValue(values[key]); value != "" {
			return value
		}
	}
	return ""
}

func stringValue(value any) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func integer(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case float64:
		return int(typed), typed == float64(int(typed))
	case string:
		number, err := strconv.Atoi(strings.TrimSpace(typed))
		return number, err == nil
	default:
		return 0, false
	}
}
