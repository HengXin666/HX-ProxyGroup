package nodeparse

import (
	"bytes"
	"crypto/sha256"
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

type Node struct {
	DisplayName string         `json:"display_name"`
	Protocol    string         `json:"protocol"`
	Fingerprint string         `json:"fingerprint"`
	Canonical   map[string]any `json:"canonical"`
}

type Failure struct {
	Index    int    `json:"index"`
	Protocol string `json:"protocol,omitempty"`
	Name     string `json:"name,omitempty"`
	Reason   string `json:"reason"`
}

type Result struct {
	DetectedFormat string    `json:"detected_format"`
	Nodes          []Node    `json:"nodes"`
	Failures       []Failure `json:"failures,omitempty"`
}

func Parse(content []byte) (Result, error) {
	return parse(content, 0)
}

func parse(content []byte, depth int) (Result, error) {
	if depth > 2 {
		return Result{}, errors.New("subscription encoding nesting is too deep")
	}
	trimmed := bytes.TrimSpace(content)
	if len(trimmed) == 0 {
		return Result{}, errors.New("subscription content is empty")
	}

	if result, ok := parseClashYAML(trimmed); ok {
		return result, nil
	}
	if result, ok := parseSingBoxJSON(trimmed); ok {
		return result, nil
	}
	if result := parseURILines(string(trimmed)); len(result.Nodes) > 0 || len(result.Failures) > 0 {
		result.DetectedFormat = "uri-list"
		return result, nil
	}
	if decoded, ok := decodeBase64(trimmed); ok {
		result, err := parse(decoded, depth+1)
		if err != nil {
			return Result{}, err
		}
		result.DetectedFormat = "base64-" + result.DetectedFormat
		return result, nil
	}
	return Result{}, errors.New("subscription format is not supported")
}

func parseSingBoxJSON(content []byte) (Result, bool) {
	var document struct {
		Outbounds []map[string]any `json:"outbounds"`
	}
	if err := json.Unmarshal(content, &document); err != nil || document.Outbounds == nil {
		return Result{}, false
	}
	result := Result{DetectedFormat: "sing-box-json", Nodes: make([]Node, 0, len(document.Outbounds))}
	for index, raw := range document.Outbounds {
		normalized, err := singBoxToCanonical(raw)
		if err != nil {
			result.Failures = append(result.Failures, Failure{
				Index: index, Protocol: strings.ToLower(strings.TrimSpace(stringValue(raw["type"]))),
				Name: stringValue(raw["tag"]), Reason: err.Error(),
			})
			continue
		}
		node, err := nodeFromClash(normalized)
		if err != nil {
			result.Failures = append(result.Failures, Failure{Index: index, Name: stringValue(raw["tag"]), Reason: err.Error()})
			continue
		}
		result.Nodes = append(result.Nodes, node)
	}
	return result, true
}

func singBoxToCanonical(raw map[string]any) (map[string]any, error) {
	input := normalizeMap(raw)
	protocol := strings.ToLower(stringValue(input["type"]))
	switch protocol {
	case "shadowsocks":
		protocol = "ss"
	case "socks":
		protocol = "socks5"
	case "vless", "vmess", "trojan", "http":
	default:
		return nil, fmt.Errorf("unsupported sing-box outbound type %q", protocol)
	}
	canonical := map[string]any{
		"name": stringValue(input["tag"]), "type": protocol,
		"server": input["server"], "port": input["server_port"],
	}
	for _, key := range []string{"uuid", "username", "password", "flow", "security"} {
		if value, exists := input[key]; exists {
			canonical[key] = value
		}
	}
	if method, exists := input["method"]; exists {
		canonical["cipher"] = method
	}
	if alterID, exists := input["alter_id"]; exists {
		canonical["alter-id"] = alterID
	}
	if tlsConfig, ok := input["tls"].(map[string]any); ok {
		if enabled, _ := tlsConfig["enabled"].(bool); enabled {
			canonical["tls"] = true
		}
		if serverName := strings.TrimSpace(stringValue(tlsConfig["server_name"])); serverName != "" {
			canonical["servername"] = serverName
		}
	}
	if transport, ok := input["transport"].(map[string]any); ok {
		transportType := strings.ToLower(stringValue(transport["type"]))
		if transportType == "ws" || transportType == "websocket" {
			canonical["network"] = "ws"
			options := map[string]any{}
			if path := strings.TrimSpace(stringValue(transport["path"])); path != "" {
				options["path"] = path
			}
			if headers, exists := transport["headers"]; exists {
				options["headers"] = headers
			}
			canonical["ws-opts"] = options
		}
	}
	return canonical, nil
}

func parseClashYAML(content []byte) (Result, bool) {
	var document struct {
		Proxies []map[string]any `yaml:"proxies"`
	}
	if err := yaml.Unmarshal(content, &document); err != nil || document.Proxies == nil {
		return Result{}, false
	}
	result := Result{DetectedFormat: "clash-yaml", Nodes: make([]Node, 0, len(document.Proxies))}
	for index, raw := range document.Proxies {
		node, err := nodeFromClash(raw)
		if err != nil {
			result.Failures = append(result.Failures, Failure{
				Index:  index,
				Name:   stringValue(raw["name"]),
				Reason: err.Error(),
			})
			continue
		}
		result.Nodes = append(result.Nodes, node)
	}
	return result, true
}

func nodeFromClash(raw map[string]any) (Node, error) {
	canonical := normalizeMap(raw)
	protocol := strings.ToLower(strings.TrimSpace(stringValue(canonical["type"])))
	if protocol == "" {
		return Node{}, errors.New("missing node type")
	}
	server := strings.TrimSpace(stringValue(canonical["server"]))
	if server == "" {
		return Node{}, errors.New("missing server")
	}
	port, err := integerValue(canonical["port"])
	if err != nil || port < 1 || port > 65535 {
		return Node{}, errors.New("invalid port")
	}
	canonical["type"] = protocol
	canonical["server"] = server
	canonical["port"] = port
	name := strings.TrimSpace(stringValue(canonical["name"]))
	if name == "" {
		name = net.JoinHostPort(server, strconv.Itoa(port))
	}
	return finalize(name, protocol, canonical)
}

func parseURILines(text string) Result {
	result := Result{}
	for index, rawLine := range strings.Split(text, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !strings.Contains(line, "://") {
			continue
		}
		node, err := parseURI(line)
		if err != nil {
			protocol := ""
			if separator := strings.Index(line, "://"); separator > 0 {
				protocol = strings.ToLower(line[:separator])
			}
			result.Failures = append(result.Failures, Failure{Index: index, Protocol: protocol, Reason: err.Error()})
			continue
		}
		result.Nodes = append(result.Nodes, node)
	}
	return result
}

func parseURI(value string) (Node, error) {
	separator := strings.Index(value, "://")
	if separator <= 0 {
		return Node{}, errors.New("invalid share URI")
	}
	protocol := strings.ToLower(value[:separator])
	switch protocol {
	case "vless", "trojan", "http", "https", "socks", "socks5":
		return parseStandardURI(value, protocol)
	case "vmess":
		return parseVMessURI(value)
	case "ss":
		return parseShadowsocksURI(value)
	default:
		return Node{}, fmt.Errorf("unsupported protocol %q", protocol)
	}
}

func parseStandardURI(value, protocol string) (Node, error) {
	parsed, err := url.Parse(value)
	if err != nil {
		return Node{}, errors.New("invalid share URI")
	}
	server := strings.TrimSpace(parsed.Hostname())
	port, err := strconv.Atoi(parsed.Port())
	if server == "" || err != nil || port < 1 || port > 65535 {
		return Node{}, errors.New("invalid server or port")
	}
	canonical := map[string]any{
		"type":   protocol,
		"server": server,
		"port":   port,
	}
	if parsed.User != nil {
		identity := parsed.User.Username()
		password, hasPassword := parsed.User.Password()
		switch protocol {
		case "vless":
			canonical["uuid"] = identity
		case "trojan":
			canonical["password"] = identity
		default:
			canonical["username"] = identity
			if hasPassword {
				canonical["password"] = password
			}
		}
	}
	if protocol == "https" {
		canonical["tls"] = true
	}
	if query := normalizeQuery(parsed.Query()); len(query) > 0 {
		canonical["query"] = query
	}
	name, _ := url.QueryUnescape(parsed.Fragment)
	if strings.TrimSpace(name) == "" {
		name = net.JoinHostPort(server, strconv.Itoa(port))
	}
	return finalize(name, protocol, canonical)
}

func parseVMessURI(value string) (Node, error) {
	decoded, ok := decodeBase64([]byte(strings.TrimPrefix(value, "vmess://")))
	if !ok {
		return Node{}, errors.New("invalid vmess base64 payload")
	}
	var raw map[string]any
	if err := json.Unmarshal(decoded, &raw); err != nil {
		return Node{}, errors.New("invalid vmess JSON payload")
	}
	canonical := normalizeMap(raw)
	server := strings.TrimSpace(stringValue(canonical["add"]))
	port, err := integerValue(canonical["port"])
	if server == "" || err != nil || port < 1 || port > 65535 {
		return Node{}, errors.New("invalid vmess server or port")
	}
	canonical["type"] = "vmess"
	canonical["server"] = server
	canonical["port"] = port
	delete(canonical, "add")
	name := strings.TrimSpace(stringValue(canonical["ps"]))
	delete(canonical, "ps")
	if name == "" {
		name = net.JoinHostPort(server, strconv.Itoa(port))
	}
	return finalize(name, "vmess", canonical)
}

func parseShadowsocksURI(value string) (Node, error) {
	payload := strings.TrimPrefix(value, "ss://")
	fragment := ""
	if index := strings.Index(payload, "#"); index >= 0 {
		fragment = payload[index+1:]
		payload = payload[:index]
	}
	query := ""
	if index := strings.Index(payload, "?"); index >= 0 {
		query = payload[index+1:]
		payload = payload[:index]
	}
	if !strings.Contains(payload, "@") {
		decoded, ok := decodeBase64([]byte(payload))
		if !ok {
			return Node{}, errors.New("invalid shadowsocks base64 payload")
		}
		payload = string(decoded)
	}
	at := strings.LastIndex(payload, "@")
	if at <= 0 {
		return Node{}, errors.New("invalid shadowsocks authority")
	}
	credentials := payload[:at]
	if !strings.Contains(credentials, ":") {
		decoded, ok := decodeBase64([]byte(credentials))
		if !ok {
			return Node{}, errors.New("invalid shadowsocks credentials")
		}
		credentials = string(decoded)
	}
	credentialSeparator := strings.Index(credentials, ":")
	if credentialSeparator <= 0 {
		return Node{}, errors.New("invalid shadowsocks credentials")
	}
	server, portText, err := net.SplitHostPort(payload[at+1:])
	if err != nil {
		return Node{}, errors.New("invalid shadowsocks server or port")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return Node{}, errors.New("invalid shadowsocks port")
	}
	canonical := map[string]any{
		"type":     "ss",
		"server":   server,
		"port":     port,
		"cipher":   credentials[:credentialSeparator],
		"password": credentials[credentialSeparator+1:],
	}
	if query != "" {
		values, _ := url.ParseQuery(query)
		canonical["query"] = normalizeQuery(values)
	}
	name, _ := url.QueryUnescape(fragment)
	if strings.TrimSpace(name) == "" {
		name = net.JoinHostPort(server, strconv.Itoa(port))
	}
	return finalize(name, "ss", canonical)
}

func finalize(name, protocol string, canonical map[string]any) (Node, error) {
	canonical = normalizeMap(canonical)
	fingerprintInput := make(map[string]any, len(canonical))
	for key, value := range canonical {
		if key != "name" {
			fingerprintInput[key] = value
		}
	}
	encoded, err := json.Marshal(fingerprintInput)
	if err != nil {
		return Node{}, fmt.Errorf("encode canonical node: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return Node{
		DisplayName: strings.TrimSpace(name),
		Protocol:    protocol,
		Fingerprint: hex.EncodeToString(digest[:]),
		Canonical:   canonical,
	}, nil
}

func normalizeMap(input map[string]any) map[string]any {
	result := make(map[string]any, len(input))
	for key, value := range input {
		result[strings.ToLower(strings.TrimSpace(key))] = normalizeValue(value)
	}
	return result
}

func normalizeValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return normalizeMap(typed)
	case map[any]any:
		converted := make(map[string]any, len(typed))
		for key, item := range typed {
			converted[fmt.Sprint(key)] = normalizeValue(item)
		}
		return normalizeMap(converted)
	case []any:
		result := make([]any, len(typed))
		for index, item := range typed {
			result[index] = normalizeValue(item)
		}
		return result
	case string:
		return strings.TrimSpace(typed)
	default:
		return value
	}
}

func normalizeQuery(values url.Values) map[string]any {
	result := make(map[string]any, len(values))
	for key, items := range values {
		if len(items) == 1 {
			result[strings.ToLower(key)] = items[0]
		} else {
			result[strings.ToLower(key)] = append([]string(nil), items...)
		}
	}
	return result
}

func decodeBase64(value []byte) ([]byte, bool) {
	compact := strings.Map(func(character rune) rune {
		if character == '\r' || character == '\n' || character == ' ' || character == '\t' {
			return -1
		}
		return character
	}, string(value))
	encodings := []*base64.Encoding{
		base64.StdEncoding,
		base64.RawStdEncoding,
		base64.URLEncoding,
		base64.RawURLEncoding,
	}
	for _, encoding := range encodings {
		decoded, err := encoding.DecodeString(compact)
		if err == nil && len(decoded) > 0 {
			return decoded, true
		}
	}
	return nil, false
}

func stringValue(value any) string {
	if value == nil {
		return ""
	}
	return fmt.Sprint(value)
}

func integerValue(value any) (int, error) {
	switch typed := value.(type) {
	case int:
		return typed, nil
	case int64:
		return int(typed), nil
	case uint64:
		return int(typed), nil
	case float64:
		return int(typed), nil
	case string:
		return strconv.Atoi(strings.TrimSpace(typed))
	default:
		return 0, errors.New("not an integer")
	}
}
