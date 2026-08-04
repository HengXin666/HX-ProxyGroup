package residential

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/HengXin666/HX-ProxyGroup/internal/listener"
	"github.com/HengXin666/HX-ProxyGroup/internal/store"
)

type ControlNode struct {
	Index       int               `json:"index"`
	NodeName    string            `json:"node_name"`
	Endpoints   []ControlEndpoint `json:"endpoints"`
	ProxyURL    *string           `json:"proxy_url"`
	Hint        string            `json:"hint,omitempty"`
	ExitIP      string            `json:"exit_ip,omitempty"`
	CountryCode string            `json:"country_code,omitempty"`
	RouteMode   string            `json:"route_mode"`
}

// ControlEndpoint describes one standard client-facing entry point for a
// declared node. Consumers can either use a browser-compatible URI directly or
// hand an advanced URI to a local data plane such as Mihomo or sing-box.
type ControlEndpoint struct {
	Protocol          string `json:"protocol"`
	Transport         string `json:"transport"`
	URI               string `json:"uri"`
	BrowserCompatible bool   `json:"browser_compatible"`
}

type ControlNodeList struct {
	Channel string        `json:"channel"`
	Nodes   []ControlNode `json:"nodes"`
}

func (s *Service) ControlNodesByToken(ctx context.Context, token string) (ControlNodeList, error) {
	channel, err := s.controlChannel(ctx, token)
	if err != nil {
		return ControlNodeList{}, err
	}
	nodes := make([]ControlNode, 0, channel.SessionCount)
	for index := 1; index <= channel.SessionCount; index++ {
		node, err := s.controlNodeView(ctx, channel, index)
		if err != nil {
			return ControlNodeList{}, err
		}
		if err := s.repository.TouchResidentialClientSession(
			ctx,
			channel.ID,
			declaredSessionID(index),
			s.now().UTC(),
		); err != nil {
			return ControlNodeList{}, err
		}
		nodes = append(nodes, node)
	}
	return ControlNodeList{Channel: channel.Name, Nodes: nodes}, nil
}

func (s *Service) RotateDeclaredSession(
	ctx context.Context,
	channelID string,
	index int,
) (ChannelSession, error) {
	channel, err := s.repository.GetResidentialChannel(ctx, channelID)
	if err != nil {
		return ChannelSession{}, mapStoreError(err)
	}
	if err := validateDeclaredIndex(channel, index); err != nil {
		return ChannelSession{}, err
	}
	if _, err := s.RotateClientSessionByToken(ctx, channel.RotateToken, declaredSessionID(index)); err != nil {
		return ChannelSession{}, err
	}
	record, err := s.repository.GetResidentialClientSession(ctx, channel.ID, declaredSessionID(index))
	if err != nil {
		return ChannelSession{}, mapStoreError(err)
	}
	return declaredSessionView(channel, record, index), nil
}

func (s *Service) RotateDeclaredSessionByControlToken(
	ctx context.Context,
	token string,
	index int,
) (ControlNode, error) {
	channel, err := s.controlChannel(ctx, token)
	if err != nil {
		return ControlNode{}, err
	}
	if err := validateDeclaredIndex(channel, index); err != nil {
		return ControlNode{}, err
	}
	if _, err := s.RotateClientSessionByToken(ctx, channel.RotateToken, declaredSessionID(index)); err != nil {
		return ControlNode{}, err
	}
	return s.controlNodeView(ctx, channel, index)
}

func (s *Service) SwitchDeclaredSessionRouteByControlToken(
	ctx context.Context,
	token string,
	index int,
	routeMode string,
) (ControlNode, error) {
	channel, err := s.controlChannel(ctx, token)
	if err != nil {
		return ControlNode{}, err
	}
	if err := validateDeclaredIndex(channel, index); err != nil {
		return ControlNode{}, err
	}
	if _, err := s.SwitchClientSessionRouteByToken(
		ctx,
		channel.RotateToken,
		declaredSessionID(index),
		routeMode,
	); err != nil {
		return ControlNode{}, err
	}
	return s.controlNodeView(ctx, channel, index)
}

func (s *Service) RotateChannelShareToken(ctx context.Context, channelID string) (Channel, error) {
	channel, err := s.repository.GetResidentialChannel(ctx, channelID)
	if err != nil {
		return Channel{}, mapStoreError(err)
	}
	if _, err := s.listeners.RotateShareToken(ctx, channel.ListenerID); err != nil {
		return Channel{}, err
	}
	return s.GetChannel(ctx, channel.ID)
}

func (s *Service) RotateChannelControlToken(ctx context.Context, channelID string) (Channel, error) {
	channel, err := s.repository.GetResidentialChannel(ctx, channelID)
	if err != nil {
		return Channel{}, mapStoreError(err)
	}
	token, err := newToken()
	if err != nil {
		return Channel{}, err
	}
	if _, err := s.repository.RotateResidentialChannelControlToken(ctx, channel.ID, token); err != nil {
		return Channel{}, mapStoreError(err)
	}
	return s.GetChannel(ctx, channel.ID)
}

func (s *Service) controlChannel(ctx context.Context, token string) (store.ResidentialChannelRecord, error) {
	token = strings.TrimSpace(token)
	if len(token) < 16 || len(token) > 128 {
		return store.ResidentialChannelRecord{}, ErrNotFound
	}
	channel, err := s.repository.GetResidentialChannelByControlToken(ctx, token)
	if err != nil {
		return store.ResidentialChannelRecord{}, mapStoreError(err)
	}
	if !channel.Enabled || channel.Mode != ModeSticky || channel.SessionCount < 1 {
		return store.ResidentialChannelRecord{}, ErrNotFound
	}
	return channel, nil
}

func validateDeclaredIndex(channel store.ResidentialChannelRecord, index int) error {
	if !channel.Enabled || channel.Mode != ModeSticky || index < 1 || index > channel.SessionCount {
		return ErrNotFound
	}
	return nil
}

func (s *Service) controlNodeView(
	ctx context.Context,
	channel store.ResidentialChannelRecord,
	index int,
) (ControlNode, error) {
	if err := validateDeclaredIndex(channel, index); err != nil {
		return ControlNode{}, err
	}
	session, err := s.repository.GetResidentialClientSession(ctx, channel.ID, declaredSessionID(index))
	if err != nil {
		return ControlNode{}, mapStoreError(err)
	}
	node := ControlNode{
		Index:       index,
		NodeName:    DeclaredNodeName(channel.Name, index),
		Endpoints:   []ControlEndpoint{},
		CountryCode: session.CountryCode,
		RouteMode:   session.RouteMode,
	}
	password, err := s.cipher.Open(
		session.AuthPasswordEncrypted,
		clientSessionAssociatedData(channel.ID, session.SessionID),
	)
	if err != nil {
		return ControlNode{}, fmt.Errorf("decrypt residential control node credential: %w", err)
	}
	node.Endpoints, err = s.controlNodeEndpoints(ctx, channel, index, session.AuthUsername, string(password))
	if err != nil {
		return ControlNode{}, err
	}
	for _, endpoint := range node.Endpoints {
		if endpoint.BrowserCompatible {
			proxyURL := browserProxyURL(endpoint.URI)
			node.ProxyURL = &proxyURL
			break
		}
	}
	if node.ProxyURL == nil {
		node.Hint = "no browser-compatible endpoint is configured; use an advanced endpoint through a local data plane"
	}
	return node, nil
}

func browserProxyURL(uri string) string {
	parsed, err := url.Parse(uri)
	if err != nil {
		return uri
	}
	parsed.Fragment = ""
	return parsed.String()
}

func (s *Service) controlNodeEndpoints(
	ctx context.Context,
	channel store.ResidentialChannelRecord,
	index int,
	username string,
	password string,
) ([]ControlEndpoint, error) {
	baseName := DeclaredNodeName(channel.Name, index)
	hasDirect := channel.DirectListenerID != ""
	type source struct {
		listenerID string
		name       string
	}
	sources := []source{{listenerID: channel.ListenerID, name: baseName}}
	if hasDirect {
		sources[0].name += "-ws"
		sources = append(sources, source{listenerID: channel.DirectListenerID, name: baseName + "-direct"})
	}

	endpoints := make([]ControlEndpoint, 0, len(sources)+1)
	for _, source := range sources {
		export, err := s.listeners.ExportWithNodes(ctx, source.listenerID, "", channel.Name, []listener.ShareNode{{
			Name: source.name,
			Auth: &listener.Auth{Username: username, Password: password},
		}})
		if errors.Is(err, listener.ErrShareDisabled) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("export control endpoint: %w", err)
		}
		transport := strings.TrimSpace(export.Transport.Type)
		if transport == "" {
			transport = "tcp"
		}
		for _, uri := range strings.Fields(export.Body) {
			protocol, _, _ := strings.Cut(uri, ":")
			protocol = strings.ToLower(protocol)
			endpoints = append(endpoints, ControlEndpoint{
				Protocol:          protocol,
				Transport:         transport,
				URI:               uri,
				BrowserCompatible: isBrowserProxyProtocol(protocol),
			})
		}
	}
	return endpoints, nil
}

func isBrowserProxyProtocol(protocol string) bool {
	switch protocol {
	case "http", "https", "socks5":
		return true
	default:
		return false
	}
}

func declaredSessionView(
	channel store.ResidentialChannelRecord,
	record store.ResidentialClientSessionRecord,
	index int,
) ChannelSession {
	return ChannelSession{
		Index:         index,
		SessionID:     record.SessionID,
		NodeName:      DeclaredNodeName(channel.Name, index),
		RouteMode:     record.RouteMode,
		CountryCode:   record.CountryCode,
		Allocated:     record.NodeFingerprint != "",
		AllocatedAt:   record.AllocatedAt,
		ExpiresAt:     record.ExpiresAt,
		RotateCount:   record.RotateCount,
		LastRotatedAt: record.LastRotatedAt,
		LastUsedAt:    record.LastUsedAt,
	}
}
