package nodeparse

import (
	"encoding/base64"
	"encoding/json"
	"testing"
)

func TestParseClashYAMLAndPreserveFailures(t *testing.T) {
	result, err := Parse([]byte(`
proxies:
  - name: HK-01
    type: vless
    server: hk.example.com
    port: 443
    uuid: 11111111-1111-1111-1111-111111111111
    tls: true
  - name: broken
    type: trojan
    server: broken.example.com
`))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if result.DetectedFormat != "clash-yaml" {
		t.Fatalf("format = %q", result.DetectedFormat)
	}
	if len(result.Nodes) != 1 || len(result.Failures) != 1 {
		t.Fatalf("nodes = %d, failures = %d", len(result.Nodes), len(result.Failures))
	}
	if result.Nodes[0].Protocol != "vless" || result.Nodes[0].DisplayName != "HK-01" {
		t.Fatalf("unexpected node: %+v", result.Nodes[0])
	}
	if result.Nodes[0].Fingerprint == "" {
		t.Fatal("fingerprint is empty")
	}
}

func TestFingerprintDoesNotDependOnDisplayName(t *testing.T) {
	first, err := Parse([]byte("vless://id@example.com:443?security=tls#first"))
	if err != nil {
		t.Fatalf("first Parse() error = %v", err)
	}
	second, err := Parse([]byte("vless://id@example.com:443?security=tls#renamed"))
	if err != nil {
		t.Fatalf("second Parse() error = %v", err)
	}
	if first.Nodes[0].Fingerprint != second.Nodes[0].Fingerprint {
		t.Fatalf("fingerprints differ: %s != %s", first.Nodes[0].Fingerprint, second.Nodes[0].Fingerprint)
	}
}

func TestParseBase64URIListAndVMess(t *testing.T) {
	vmessPayload, err := json.Marshal(map[string]any{
		"v":    "2",
		"ps":   "Tokyo",
		"add":  "jp.example.com",
		"port": "443",
		"id":   "22222222-2222-2222-2222-222222222222",
		"net":  "ws",
		"tls":  "tls",
	})
	if err != nil {
		t.Fatal(err)
	}
	list := "trojan://secret@sg.example.com:443#SG\nvmess://" + base64.RawStdEncoding.EncodeToString(vmessPayload)
	wrapped := base64.StdEncoding.EncodeToString([]byte(list))
	result, err := Parse([]byte(wrapped))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if result.DetectedFormat != "base64-uri-list" {
		t.Fatalf("format = %q", result.DetectedFormat)
	}
	if len(result.Nodes) != 2 {
		t.Fatalf("nodes = %d", len(result.Nodes))
	}
}

func TestStandardURIUsesProtocolSpecificCredentialFields(t *testing.T) {
	vless, err := Parse([]byte("vless://11111111-1111-1111-1111-111111111111@example.com:443#vless"))
	if err != nil {
		t.Fatal(err)
	}
	if vless.Nodes[0].Canonical["uuid"] == nil || vless.Nodes[0].Canonical["username"] != nil {
		t.Fatalf("unexpected vless canonical fields: %#v", vless.Nodes[0].Canonical)
	}
	trojan, err := Parse([]byte("trojan://secret@example.com:443#trojan"))
	if err != nil {
		t.Fatal(err)
	}
	if trojan.Nodes[0].Canonical["password"] != "secret" || trojan.Nodes[0].Canonical["username"] != nil {
		t.Fatalf("unexpected trojan canonical fields: %#v", trojan.Nodes[0].Canonical)
	}
}

func TestParseShadowsocksSIP002(t *testing.T) {
	credentials := base64.RawURLEncoding.EncodeToString([]byte("aes-128-gcm:password"))
	result, err := Parse([]byte("ss://" + credentials + "@example.com:8388#SS"))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if len(result.Nodes) != 1 || result.Nodes[0].Protocol != "ss" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestUnsupportedURIIsRetainedAsFailure(t *testing.T) {
	result, err := Parse([]byte("unknown://example.com:1234#bad"))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if len(result.Nodes) != 0 || len(result.Failures) != 1 {
		t.Fatalf("unexpected result: %+v", result)
	}
	if result.Failures[0].Protocol != "unknown" {
		t.Fatalf("failure protocol = %q", result.Failures[0].Protocol)
	}
}
