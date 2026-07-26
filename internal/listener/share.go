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
)

// ErrShareDisabled marks share exports rejected because the listener is
// disabled; the API maps it onto 404 to avoid leaking listener existence.
var ErrShareDisabled = errors.New("listener share link is disabled")

// ShareExport is the rendered subscription payload for one listener.
type ShareExport struct {
	// Body is the plain URI list, one proxy URI per line.
	Body string
	// FileName is a suggested download name.
	FileName string
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
// used to reach the control plane; it substitutes loopback/unspecified bind
// addresses so the generated URIs stay importable from other machines.
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
	host := exportHost(record.BindAddress, requestHost)
	uris := shareURIs(record.Kind, record.Name, host, record.Port, auth)
	return ShareExport{
		Body:     strings.Join(uris, "\n") + "\n",
		FileName: sanitizeFileName(record.Name) + ".txt",
	}, nil
}

// EncodeSubscription renders the conventional base64 subscription body used
// by most proxy clients.
func (export ShareExport) EncodeSubscription() string {
	return base64.StdEncoding.EncodeToString([]byte(export.Body))
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

func shareURIs(kind, name, host string, port int, auth *Auth) []string {
	var schemes []string
	switch kind {
	case "http":
		schemes = []string{"http"}
	case "socks":
		schemes = []string{"socks5"}
	default: // mixed exposes both entry protocols on one port
		schemes = []string{"http", "socks5"}
	}
	uris := make([]string, 0, len(schemes))
	for _, scheme := range schemes {
		address := url.URL{
			Scheme:   scheme,
			Host:     net.JoinHostPort(host, strconv.Itoa(port)),
			Fragment: name,
		}
		if auth != nil {
			address.User = url.UserPassword(auth.Username, auth.Password)
		}
		uris = append(uris, address.String())
	}
	return uris
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
