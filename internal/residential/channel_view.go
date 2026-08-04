package residential

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/HengXin666/HX-ProxyGroup/internal/listener"
	"github.com/HengXin666/HX-ProxyGroup/internal/store"
)

func (s *Service) ListChannels(ctx context.Context) ([]Channel, error) {
	records, err := s.repository.ListResidentialChannels(ctx)
	if err != nil {
		return nil, err
	}
	providers, err := s.repository.ListResidentialProviders(ctx)
	if err != nil {
		return nil, err
	}
	providerRecords := make(map[string]store.ResidentialProviderRecord, len(providers))
	for _, provider := range providers {
		providerRecords[provider.ID] = provider
	}
	channels := make([]Channel, 0, len(records))
	for _, record := range records {
		channel, err := s.channelFromRecord(ctx, record, s.providerFromRecord(providerRecords[record.ProviderID]))
		if err != nil {
			return nil, err
		}
		channels = append(channels, channel)
	}
	return channels, nil
}

func (s *Service) GetChannel(ctx context.Context, id string) (Channel, error) {
	record, err := s.repository.GetResidentialChannel(ctx, id)
	if err != nil {
		return Channel{}, mapStoreError(err)
	}
	provider, err := s.repository.GetResidentialProvider(ctx, record.ProviderID)
	if err != nil {
		return Channel{}, mapStoreError(err)
	}
	return s.channelFromRecord(ctx, record, s.providerFromRecord(provider))
}

func (s *Service) channelFromRecord(ctx context.Context, record store.ResidentialChannelRecord, provider Provider) (Channel, error) {
	pool, err := s.repository.ListResidentialSessionNodes(ctx, record.ID)
	if err != nil {
		return Channel{}, err
	}
	activeSessionCount := 0
	if record.Mode == ModeSticky {
		sessions, err := s.repository.ListResidentialClientSessions(ctx, record.ID)
		if err != nil {
			return Channel{}, err
		}
		for _, session := range sessions {
			if session.RouteMode == ClientRouteResidential && session.NodeFingerprint != "" {
				activeSessionCount++
			}
		}
	} else {
		activeSessionCount = len(pool)
	}
	channel := Channel{
		ID: record.ID, Name: record.Name, ProviderID: record.ProviderID, ProviderName: provider.Name,
		Mode: record.Mode, ProxyGroupID: record.ProxyGroupID, ListenerID: record.ListenerID,
		Region: record.Region, RegionMode: normalizedChannelRegionMode(record.RegionMode),
		RandomRegions: parseRegionList(record.RandomRegions), SessionCount: record.SessionCount,
		IdleReleaseSeconds: record.IdleReleaseSeconds, ActiveSessionCount: activeSessionCount,
		PoolSize: len(pool), ActiveSessionIndex: record.ActiveSessionIndex, RotateCount: record.RotateCount,
		LastRotatedAt: record.LastRotatedAt, LastExitIP: record.LastExitIP, PoolCreatedAt: record.PoolCreatedAt,
		PoolRefreshAfterSeconds: int(SessionPoolRefreshAge(provider).Seconds()),
		SessionTTLSeconds:       provider.SessionTTLSeconds, Enabled: record.Enabled, Version: record.Version,
		CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt,
	}
	if record.Mode == ModeSticky && record.RotateToken != "" {
		channel.RotatePath = "/rot/" + record.RotateToken
	}
	if record.Mode == ModeSticky && record.ControlToken != "" {
		channel.ControlPath = "/ctl/" + record.ControlToken
	}
	if record.Mode == ModeSticky && record.SessionCount > 0 {
		sessionViews, err := s.declaredSessionViews(ctx, record)
		if err != nil {
			return Channel{}, err
		}
		channel.Sessions = sessionViews
	}
	if record.DirectListenerID != "" {
		directRecord, err := s.repository.GetListener(ctx, record.DirectListenerID)
		if err == nil {
			channel.DirectEndpoint = &ChannelEndpoint{
				Kind: directRecord.Kind, BindAddress: directRecord.BindAddress, Port: directRecord.Port,
				AuthEnabled: directRecord.AuthMode != "none" && len(directRecord.AuthConfigEncrypted) > 0,
			}
		} else if !errors.Is(err, store.ErrNotFound) {
			return Channel{}, err
		}
	}
	listenerRecord, err := s.repository.GetListener(ctx, record.ListenerID)
	if err == nil {
		var transport listener.Transport
		if err := json.Unmarshal([]byte(listenerRecord.TransportJSON), &transport); err != nil {
			return Channel{}, fmt.Errorf("decode residential listener transport: %w", err)
		}
		channel.Endpoint = ChannelEndpoint{
			Kind: listenerRecord.Kind, BindAddress: listenerRecord.BindAddress, Port: listenerRecord.Port,
			AuthEnabled: listenerRecord.AuthMode != "none" && len(listenerRecord.AuthConfigEncrypted) > 0,
			Transport:   transport,
		}
		if err := json.Unmarshal([]byte(listenerRecord.PublicEndpointJSON), &channel.PublicEndpoint); err != nil {
			return Channel{}, fmt.Errorf("decode residential public endpoint: %w", err)
		}
		if listenerRecord.ShareToken != "" {
			channel.Endpoint.SharePath = "/sub/" + listenerRecord.ShareToken
			publishable := record.Mode == ModePassthrough && isResidentialWebSocketKind(listenerRecord.Kind)
			if record.Mode == ModeSticky && record.SessionCount > 0 {
				publishable = true
			}
			if publishable {
				channel.SubscriptionURL = listener.PublicPathURL(
					channel.PublicEndpoint,
					channel.Endpoint.SharePath+"?format=clash",
				)
			}
		}
		if channel.RotatePath != "" && channel.PublicEndpoint.TLS {
			channel.RotationURL = listener.PublicPathURL(channel.PublicEndpoint, channel.RotatePath)
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		return Channel{}, err
	}
	return channel, nil
}

func channelRegionSelection(mode RegionMode, region string, randomRegions []string, provider Provider) (RegionSelection, error) {
	if strings.TrimSpace(string(mode)) == "" && strings.TrimSpace(region) == "" && len(randomRegions) == 0 {
		selection := providerRegionSelection(provider)
		return normalizeRegionSelection(string(selection.Mode), selection.Region, selection.RandomRegions)
	}
	return normalizeRegionSelection(string(mode), region, randomRegions)
}

func normalizedChannelRegionMode(mode string) RegionMode {
	if mode == "" {
		return RegionModeFixed
	}
	return RegionMode(mode)
}
