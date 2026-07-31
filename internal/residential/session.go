package residential

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
)

// Credentials are the vendor account secrets. They are stored AEAD-encrypted and
// never returned by the API.
type Credentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// Session is one pooled sticky session of a channel.
type Session struct {
	// Index is the stable slot number inside the channel pool. The channel's
	// active_session_index points at one of these.
	Index int `json:"index"`
	// ID is the sticky session identifier sent to the vendor.
	ID string `json:"id"`
	// Username is the fully rendered gateway username.
	Username string `json:"-"`
}

// newSessionID returns an opaque sticky-session identifier. It is hex so it
// survives every vendor username dialect without escaping.
func newSessionID() (string, error) {
	var buffer [8]byte
	if _, err := rand.Read(buffer[:]); err != nil {
		return "", fmt.Errorf("generate residential session id: %w", err)
	}
	return hex.EncodeToString(buffer[:]), nil
}

// buildSessions renders a pool of sticky sessions for one channel.
//
// For per-request gateways there is no session to pin, so exactly one session is
// produced and the pool degenerates to a single upstream.
func buildSessions(provider Provider, credentials Credentials, region string, size int) ([]Session, error) {
	if provider.RotationMode != RotationSessionTemplate || !TemplateUsesSession(provider.UsernameTemplate) {
		username, err := Render(provider.UsernameTemplate, Variables{
			User:    credentials.Username,
			Region:  region,
			Country: region,
			TTL:     strconv.Itoa(provider.SessionTTLSeconds),
		})
		if err != nil {
			return nil, err
		}
		return []Session{{Index: 0, ID: "", Username: username}}, nil
	}
	if size < 1 {
		size = 1
	}
	sessions := make([]Session, 0, size)
	for index := 0; index < size; index++ {
		sessionID, err := newSessionID()
		if err != nil {
			return nil, err
		}
		username, err := Render(provider.UsernameTemplate, Variables{
			User:    credentials.Username,
			Session: sessionID,
			Region:  region,
			Country: region,
			TTL:     strconv.Itoa(provider.SessionTTLSeconds),
		})
		if err != nil {
			return nil, err
		}
		sessions = append(sessions, Session{Index: index, ID: sessionID, Username: username})
	}
	return sessions, nil
}

// canonicalNodeConfig renders one session as a canonical node configuration.
//
// The shape matches what internal/nodeparse produces for subscription nodes so
// the existing Mihomo compiler path converts it without special cases. Upstream
// residential gateways are plain HTTP/SOCKS5 proxies with credentials, so no new
// protocol support is required.
func canonicalNodeConfig(provider Provider, session Session, password, displayName string) map[string]any {
	protocol := provider.Protocol
	config := map[string]any{
		"name":     displayName,
		"type":     protocol,
		"server":   provider.GatewayHost,
		"port":     provider.GatewayPort,
		"username": session.Username,
		"password": password,
	}
	if protocol == "https" {
		config["type"] = "http"
		config["tls"] = true
	}
	if protocol == "socks5" {
		// Residential gateways commonly proxy UDP as well; Mihomo ignores the
		// hint when the gateway refuses it.
		config["udp"] = true
	}
	return config
}

// sessionFingerprint derives the stable node fingerprint for a session.
//
// The channel id participates so two channels of the same provider never share a
// node row, and the display name is excluded so renaming a channel does not
// orphan traffic history. Credentials are hashed, never stored in the clear.
func sessionFingerprint(channelID string, provider Provider, session Session) (string, error) {
	payload := map[string]any{
		"channel":  channelID,
		"type":     provider.Protocol,
		"server":   provider.GatewayHost,
		"port":     provider.GatewayPort,
		"username": session.Username,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encode residential session fingerprint: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

// sessionDisplayName builds a human-readable, stably sortable node name. The
// zero-padded index keeps the pool ordering identical between the database sort
// and the rotation index.
func sessionDisplayName(channelName, region string, session Session) string {
	label := channelName
	if region != "" {
		label += " " + region
	}
	return fmt.Sprintf("%s #%02d", label, session.Index+1)
}
