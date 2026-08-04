package residential

import (
	"context"
	"fmt"
	"time"

	"github.com/HengXin666/HX-ProxyGroup/internal/proxygroup"
	"github.com/HengXin666/HX-ProxyGroup/internal/store"
)

func sessionAllocationExpired(record store.ResidentialClientSessionRecord, now time.Time) bool {
	return record.RouteMode == ClientRouteResidential && record.ExpiresAt != nil &&
		!now.UTC().Before(record.ExpiresAt.UTC())
}

func (s *Service) allocateClientSessionNode(ctx context.Context, channel store.ResidentialChannelRecord, providerRecord store.ResidentialProviderRecord, logicalSessionID string, allocatedAt time.Time, countryCode string) (string, *time.Time, error) {
	provider := s.providerFromRecord(providerRecord)
	credentials, err := s.providerCredentials(providerRecord)
	if err != nil {
		return "", nil, err
	}
	regionSelection, err := clientSessionRegionSelection(channel, countryCode)
	if err != nil {
		return "", nil, err
	}
	sessions, err := s.providerSessions(ctx, provider, credentials, regionSelection, 1)
	if err != nil {
		return "", nil, fmt.Errorf("allocate residential IP: %w", err)
	}
	if len(sessions) == 0 {
		return "", nil, fmt.Errorf("%w: provider returned no residential IP", ErrInvalid)
	}
	session := sessions[0]
	fingerprint, err := sessionFingerprint(channel.ID, provider, session)
	if err != nil {
		return "", nil, err
	}
	displayName := channel.Name + " session " + logicalSessionID
	canonical := canonicalNodeConfig(provider, session, credentials.Password, displayName)
	encrypted, err := s.sealNodeConfig(canonical, fingerprint)
	if err != nil {
		return "", nil, err
	}
	nodeID, err := newID("node-residential")
	if err != nil {
		return "", nil, err
	}
	protocol := provider.Protocol
	if protocol == "https" {
		protocol = "http"
	}
	if _, err := s.repository.UpsertResidentialSessionNode(ctx, channel.ID, store.ResidentialSessionNode{
		ID: nodeID, Fingerprint: fingerprint, DisplayName: displayName,
		Protocol: protocol, CanonicalConfigEncrypted: encrypted,
	}, allocatedAt); err != nil {
		return "", nil, err
	}
	var expiresAt *time.Time
	if lifetime := SessionPoolLifetime(provider); lifetime > 0 {
		value := allocatedAt.UTC().Add(lifetime)
		expiresAt = &value
	}
	return fingerprint, expiresAt, nil
}

func (s *Service) republishClientSessionGroup(ctx context.Context, channel store.ResidentialChannelRecord) error {
	group, err := s.repository.GetProxyGroup(ctx, channel.ProxyGroupID)
	if err != nil {
		return mapStoreError(err)
	}
	nodes, err := s.repository.ListResidentialSessionNodes(ctx, channel.ID)
	if err != nil {
		return err
	}
	sessions, err := s.repository.ListResidentialClientSessions(ctx, channel.ID)
	if err != nil {
		return err
	}
	referenced := make(map[string]struct{}, len(sessions))
	for _, session := range sessions {
		if session.RouteMode == ClientRouteResidential && session.NodeFingerprint != "" {
			referenced[session.NodeFingerprint] = struct{}{}
		}
	}
	nodeIDs := make([]string, 0, len(referenced))
	for _, node := range nodes {
		if _, ok := referenced[node.Fingerprint]; ok || channel.Mode == ModePassthrough {
			nodeIDs = append(nodeIDs, node.ID)
		}
	}
	_, err = s.groups.Update(ctx, group.ID, proxygroup.UpdateRequest{
		Version: group.Version, Name: group.Name, Strategy: group.Strategy,
		SourceSpec: proxygroup.SourceSpec{NodeIDs: nodeIDs, AllowEmpty: channel.Mode == ModeSticky},
		Enabled:    group.Enabled, EmptyBehavior: group.EmptyBehavior,
	})
	if err != nil {
		return err
	}
	return s.applyClientSessionRoutes(ctx)
}

func (s *Service) replaceClientSessionAllocation(ctx context.Context, channel store.ResidentialChannelRecord, provider store.ResidentialProviderRecord, previous store.ResidentialClientSessionRecord, rotated bool) (store.ResidentialClientSessionRecord, error) {
	now := s.now().UTC()
	fingerprint, expiresAt, err := s.allocateClientSessionNode(ctx, channel, provider, previous.SessionID, now, previous.CountryCode)
	if err != nil {
		return store.ResidentialClientSessionRecord{}, err
	}
	updated, err := s.repository.UpdateResidentialClientSessionAllocation(ctx, channel.ID, previous.SessionID, fingerprint, now, expiresAt, rotated)
	if err != nil {
		_ = s.repository.DeleteResidentialSessionNode(ctx, channel.ID, fingerprint)
		return store.ResidentialClientSessionRecord{}, mapStoreError(err)
	}
	if err := s.republishClientSessionGroup(ctx, channel); err != nil {
		_ = s.repository.RestoreResidentialClientSessionState(ctx, previous)
		_ = s.repository.DeleteResidentialSessionNode(ctx, channel.ID, fingerprint)
		_ = s.republishClientSessionGroup(ctx, channel)
		return store.ResidentialClientSessionRecord{}, fmt.Errorf("publish residential client allocation: %w", err)
	}
	if previous.NodeFingerprint != "" && previous.NodeFingerprint != fingerprint {
		_ = s.repository.DeleteResidentialSessionNode(ctx, channel.ID, previous.NodeFingerprint)
	}
	if err := s.closeChannelClientConnections(ctx, channel, updated.AuthUsername); err != nil {
		return store.ResidentialClientSessionRecord{}, err
	}
	return updated, nil
}

func (s *Service) deleteClientSession(ctx context.Context, channel store.ResidentialChannelRecord, current store.ResidentialClientSessionRecord) error {
	if err := s.repository.DeleteResidentialClientSession(ctx, channel.ID, current.SessionID); err != nil {
		return mapStoreError(err)
	}
	if err := s.republishClientSessionGroup(ctx, channel); err != nil {
		_, restoreErr := s.repository.CreateResidentialClientSession(ctx, current)
		_ = s.republishClientSessionGroup(ctx, channel)
		return fmt.Errorf("remove residential client session route: %w; restore: %v", err, restoreErr)
	}
	if current.NodeFingerprint != "" {
		_ = s.repository.DeleteResidentialSessionNode(ctx, channel.ID, current.NodeFingerprint)
	}
	return s.closeChannelClientConnections(ctx, channel, current.AuthUsername)
}
