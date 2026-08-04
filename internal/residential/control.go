package residential

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strings"

	"github.com/HengXin666/HX-ProxyGroup/internal/listener"
	"github.com/HengXin666/HX-ProxyGroup/internal/store"
)

type ControlNode struct {
	Index       int     `json:"index"`
	NodeName    string  `json:"node_name"`
	ProxyURL    *string `json:"proxy_url"`
	Hint        string  `json:"hint,omitempty"`
	ExitIP      string  `json:"exit_ip,omitempty"`
	CountryCode string  `json:"country_code,omitempty"`
	RouteMode   string  `json:"route_mode"`
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
		CountryCode: session.CountryCode,
		RouteMode:   session.RouteMode,
	}
	if channel.DirectListenerID == "" {
		node.Hint = "no direct HTTP/SOCKS listener is configured; import the subscription into a local Mihomo client"
		return node, nil
	}
	direct, err := s.repository.GetListener(ctx, channel.DirectListenerID)
	if err != nil {
		return ControlNode{}, mapStoreError(err)
	}
	if !direct.Enabled {
		node.Hint = "the direct HTTP/SOCKS listener is disabled"
		return node, nil
	}
	password, err := s.cipher.Open(
		session.AuthPasswordEncrypted,
		clientSessionAssociatedData(channel.ID, session.SessionID),
	)
	if err != nil {
		return ControlNode{}, fmt.Errorf("decrypt residential control node credential: %w", err)
	}
	scheme := "http"
	switch direct.Kind {
	case "socks":
		scheme = "socks5"
	case "http", "mixed":
	default:
		node.Hint = "the direct listener protocol is not supported by browser automation"
		return node, nil
	}
	host := direct.BindAddress
	port := direct.Port
	var endpoint listener.PublicEndpoint
	if err := json.Unmarshal([]byte(direct.PublicEndpointJSON), &endpoint); err == nil && endpoint.Host != "" {
		host = endpoint.Host
		if endpoint.Port > 0 {
			port = endpoint.Port
		}
		if endpoint.TLS && scheme == "http" {
			scheme = "https"
		}
	}
	proxyURL := (&url.URL{
		Scheme: scheme,
		Host:   net.JoinHostPort(host, fmt.Sprintf("%d", port)),
		User:   url.UserPassword(session.AuthUsername, string(password)),
	}).String()
	node.ProxyURL = &proxyURL
	return node, nil
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
