package residential

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/HengXin666/HX-ProxyGroup/internal/store"
)

const (
	ClientRouteResidential = "residential"
	ClientRouteDirect      = "direct"
	ClientRouteUpstream    = "upstream"
)

// ClientSession is the public, token-authorized view of one logical caller.
// ProxyPassword is returned only by EnsureClientSessionByToken so a caller can
// configure its browser; status and route operations omit it.
type ClientSession struct {
	SessionID     string     `json:"session_id"`
	ProxyUsername string     `json:"proxy_username"`
	ProxyPassword string     `json:"proxy_password,omitempty"`
	RouteMode     string     `json:"route_mode"`
	SessionIndex  int        `json:"session_index"`
	PoolSize      int        `json:"pool_size,omitempty"` // pre-v19 response compatibility
	AllocatedAt   *time.Time `json:"allocated_at,omitempty"`
	ExpiresAt     *time.Time `json:"expires_at,omitempty"`
	RotateCount   int        `json:"rotate_count"`
	LastRotatedAt *time.Time `json:"last_rotated_at,omitempty"`
}

func (s *Service) EnsureClientSessionByToken(
	ctx context.Context,
	token, sessionID string,
) (ClientSession, error) {
	s.clientSessionMutex.Lock()
	defer s.clientSessionMutex.Unlock()
	record, err := s.clientSessionChannel(ctx, token, sessionID)
	if err != nil {
		return ClientSession{}, err
	}
	existing, err := s.repository.GetResidentialClientSession(ctx, record.ID, sessionID)
	if err == nil {
		if sessionAllocationExpired(existing, s.now()) {
			providerRecord, providerErr := s.repository.GetResidentialProvider(ctx, record.ProviderID)
			if providerErr != nil {
				return ClientSession{}, mapStoreError(providerErr)
			}
			if providerRecord.SessionExpiryPolicy == "expire" {
				if err := s.deleteClientSession(ctx, record, existing); err != nil {
					return ClientSession{}, err
				}
				return ClientSession{}, ErrSessionExpired
			}
			updated, replaceErr := s.replaceClientSessionAllocation(ctx, record, providerRecord, existing, true)
			if replaceErr != nil {
				return ClientSession{}, replaceErr
			}
			return s.clientSessionView(ctx, updated, true)
		}
		return s.clientSessionView(ctx, existing, true)
	}
	if !errors.Is(err, store.ErrNotFound) {
		return ClientSession{}, err
	}
	providerRecord, err := s.repository.GetResidentialProvider(ctx, record.ProviderID)
	if err != nil {
		return ClientSession{}, mapStoreError(err)
	}
	sessions, err := s.repository.ListResidentialClientSessions(ctx, record.ID)
	if err != nil {
		return ClientSession{}, err
	}
	activeSessions := 0
	for _, session := range sessions {
		if session.RouteMode == ClientRouteResidential {
			activeSessions++
		}
	}
	if activeSessions >= providerRecord.PoolSize {
		return ClientSession{}, fmt.Errorf("%w: provider concurrent session limit reached", ErrConflict)
	}
	password, err := newToken()
	if err != nil {
		return ClientSession{}, err
	}
	username := clientSessionUsername(record.ID, sessionID)
	encrypted, err := s.cipher.Seal([]byte(password), clientSessionAssociatedData(record.ID, sessionID))
	if err != nil {
		return ClientSession{}, fmt.Errorf("encrypt residential client password: %w", err)
	}
	now := s.now().UTC()
	fingerprint, expiresAt, err := s.allocateClientSessionNode(ctx, record, providerRecord, sessionID, now)
	if err != nil {
		return ClientSession{}, err
	}
	created, err := s.repository.CreateResidentialClientSession(ctx, store.ResidentialClientSessionRecord{
		ChannelID:             record.ID,
		SessionID:             sessionID,
		AuthUsername:          username,
		AuthPasswordEncrypted: encrypted,
		SessionIndex:          -1,
		NodeFingerprint:       fingerprint,
		RouteMode:             ClientRouteResidential,
		AllocatedAt:           &now,
		ExpiresAt:             expiresAt,
		CreatedAt:             now,
		UpdatedAt:             now,
	})
	if err != nil {
		_ = s.repository.DeleteResidentialSessionNode(ctx, record.ID, fingerprint)
		return ClientSession{}, mapStoreError(err)
	}
	if err := s.republishClientSessionGroup(ctx, record); err != nil {
		_ = s.repository.DeleteResidentialClientSession(ctx, record.ID, sessionID)
		_ = s.repository.DeleteResidentialSessionNode(ctx, record.ID, fingerprint)
		_ = s.republishClientSessionGroup(ctx, record)
		return ClientSession{}, fmt.Errorf("publish residential client session: %w", err)
	}
	view, err := s.clientSessionView(ctx, created, false)
	if err != nil {
		return ClientSession{}, err
	}
	view.ProxyPassword = password
	return view, nil
}

func (s *Service) GetClientSessionByToken(
	ctx context.Context,
	token, sessionID string,
) (ClientSession, error) {
	record, err := s.clientSessionChannel(ctx, token, sessionID)
	if err != nil {
		return ClientSession{}, err
	}
	session, err := s.repository.GetResidentialClientSession(ctx, record.ID, sessionID)
	if err != nil {
		return ClientSession{}, mapStoreError(err)
	}
	return s.clientSessionView(ctx, session, false)
}

func (s *Service) RotateClientSessionByToken(
	ctx context.Context,
	token, sessionID string,
) (ClientSession, error) {
	s.clientSessionMutex.Lock()
	defer s.clientSessionMutex.Unlock()
	channel, err := s.clientSessionChannel(ctx, token, sessionID)
	if err != nil {
		return ClientSession{}, err
	}
	if wait, allowed := s.rotateLimiter.allow(channel.ID + "\x00" + sessionID); !allowed {
		return ClientSession{}, fmt.Errorf("%w: retry in %s", ErrRateLimited, wait.Round(time.Millisecond))
	}
	current, err := s.repository.GetResidentialClientSession(ctx, channel.ID, sessionID)
	if err != nil {
		return ClientSession{}, mapStoreError(err)
	}
	if current.RouteMode != ClientRouteResidential {
		return ClientSession{}, fmt.Errorf("%w: client session is routed direct", ErrInvalid)
	}
	provider, err := s.repository.GetResidentialProvider(ctx, channel.ProviderID)
	if err != nil {
		return ClientSession{}, mapStoreError(err)
	}
	updated, err := s.replaceClientSessionAllocation(ctx, channel, provider, current, true)
	if err != nil {
		return ClientSession{}, err
	}
	return s.clientSessionView(ctx, updated, false)
}

func (s *Service) SwitchClientSessionRouteByToken(
	ctx context.Context,
	token, sessionID, routeMode string,
) (ClientSession, error) {
	s.clientSessionMutex.Lock()
	defer s.clientSessionMutex.Unlock()
	channel, err := s.clientSessionChannel(ctx, token, sessionID)
	if err != nil {
		return ClientSession{}, err
	}
	routeMode = strings.ToLower(strings.TrimSpace(routeMode))
	if routeMode != ClientRouteResidential && routeMode != ClientRouteDirect && routeMode != ClientRouteUpstream {
		return ClientSession{}, fmt.Errorf("%w: route_mode must be residential, upstream or direct", ErrInvalid)
	}
	current, err := s.repository.GetResidentialClientSession(ctx, channel.ID, sessionID)
	if err != nil {
		return ClientSession{}, mapStoreError(err)
	}
	if current.RouteMode == routeMode {
		return s.clientSessionView(ctx, current, false)
	}
	index := -1
	if routeMode == ClientRouteResidential {
		if current.NodeFingerprint == "" || sessionAllocationExpired(current, s.now()) {
			provider, err := s.repository.GetResidentialProvider(ctx, channel.ProviderID)
			if err != nil {
				return ClientSession{}, mapStoreError(err)
			}
			updated, err := s.replaceClientSessionAllocation(ctx, channel, provider, current, false)
			if err != nil {
				return ClientSession{}, err
			}
			return s.clientSessionView(ctx, updated, false)
		}
	} else if routeMode == ClientRouteUpstream {
		provider, err := s.repository.GetResidentialProvider(ctx, channel.ProviderID)
		if err != nil {
			return ClientSession{}, mapStoreError(err)
		}
		if provider.UpstreamProxyGroupID == "" {
			return ClientSession{}, fmt.Errorf("%w: provider has no upstream proxy group", ErrInvalid)
		}
		group, err := s.repository.GetProxyGroup(ctx, provider.UpstreamProxyGroupID)
		if err != nil || !group.Enabled {
			return ClientSession{}, fmt.Errorf("%w: provider upstream proxy group is unavailable", ErrInvalid)
		}
	}
	updated, err := s.repository.UpdateResidentialClientSessionRoute(
		ctx, channel.ID, sessionID, routeMode, index, nil,
	)
	if err != nil {
		return ClientSession{}, mapStoreError(err)
	}
	if err := s.commitClientSessionRoute(ctx, current, channel.ListenerID, updated.AuthUsername); err != nil {
		return ClientSession{}, err
	}
	if routeMode != ClientRouteResidential && current.NodeFingerprint != "" {
		_ = s.repository.DeleteResidentialSessionNode(ctx, channel.ID, current.NodeFingerprint)
		if err := s.republishClientSessionGroup(ctx, channel); err != nil {
			return ClientSession{}, fmt.Errorf("release residential client allocation: %w", err)
		}
	}
	return s.clientSessionView(ctx, updated, false)
}

func (s *Service) DeleteClientSessionByToken(ctx context.Context, token, sessionID string) error {
	s.clientSessionMutex.Lock()
	defer s.clientSessionMutex.Unlock()
	channel, err := s.clientSessionChannel(ctx, token, sessionID)
	if err != nil {
		return err
	}
	current, err := s.repository.GetResidentialClientSession(ctx, channel.ID, sessionID)
	if err != nil {
		return mapStoreError(err)
	}
	return s.deleteClientSession(ctx, channel, current)
}

// MaintainExpiredClientSessions enforces provider TTLs even when a client does
// not call the session API again. Work is bounded and serialized with public
// allocation/rotation requests.
func (s *Service) MaintainExpiredClientSessions(ctx context.Context, limit int) (int, error) {
	if limit < 1 {
		return 0, nil
	}
	s.clientSessionMutex.Lock()
	defer s.clientSessionMutex.Unlock()
	channels, err := s.repository.ListResidentialChannels(ctx)
	if err != nil {
		return 0, err
	}
	processed := 0
	for _, channel := range channels {
		if processed >= limit {
			break
		}
		provider, err := s.repository.GetResidentialProvider(ctx, channel.ProviderID)
		if err != nil {
			return processed, mapStoreError(err)
		}
		sessions, err := s.repository.ListResidentialClientSessions(ctx, channel.ID)
		if err != nil {
			return processed, err
		}
		for _, session := range sessions {
			if processed >= limit {
				break
			}
			if err := ctx.Err(); err != nil {
				return processed, err
			}
			if !sessionAllocationExpired(session, s.now()) {
				continue
			}
			if provider.SessionExpiryPolicy == "expire" {
				err = s.deleteClientSession(ctx, channel, session)
			} else {
				_, err = s.replaceClientSessionAllocation(ctx, channel, provider, session, true)
			}
			if err != nil {
				return processed, err
			}
			processed++
		}
	}
	return processed, nil
}

func (s *Service) clientSessionChannel(
	ctx context.Context,
	token, sessionID string,
) (store.ResidentialChannelRecord, error) {
	if err := validateClientSessionID(sessionID); err != nil {
		return store.ResidentialChannelRecord{}, err
	}
	record, err := s.channelByToken(ctx, token)
	if err != nil {
		return store.ResidentialChannelRecord{}, err
	}
	if record.Mode != ModeSticky {
		return store.ResidentialChannelRecord{}, ErrNotFound
	}
	return record, nil
}

func (s *Service) clientSessionView(
	ctx context.Context,
	record store.ResidentialClientSessionRecord,
	includePassword bool,
) (ClientSession, error) {
	view := ClientSession{
		SessionID: record.SessionID, ProxyUsername: record.AuthUsername,
		RouteMode: record.RouteMode, SessionIndex: record.SessionIndex,
		RotateCount: record.RotateCount, LastRotatedAt: record.LastRotatedAt,
		AllocatedAt: record.AllocatedAt, ExpiresAt: record.ExpiresAt,
	}
	if includePassword {
		plaintext, err := s.cipher.Open(
			record.AuthPasswordEncrypted,
			clientSessionAssociatedData(record.ChannelID, record.SessionID),
		)
		if err != nil {
			return ClientSession{}, fmt.Errorf("decrypt residential client password: %w", err)
		}
		view.ProxyPassword = string(plaintext)
	}
	return view, nil
}

func (s *Service) commitClientSessionRoute(
	ctx context.Context,
	previous store.ResidentialClientSessionRecord,
	listenerID string,
	authUsername string,
) error {
	if err := s.applyClientSessionRoutes(ctx); err != nil {
		restoreErr := s.repository.RestoreResidentialClientSessionState(ctx, previous)
		if restoreErr == nil {
			restoreErr = s.applyClientSessionRoutes(ctx)
		}
		return fmt.Errorf("publish residential client route: %w; rollback: %v", err, restoreErr)
	}
	return s.closeClientSessionConnections(ctx, listenerID, authUsername)
}

func (s *Service) applyClientSessionRoutes(ctx context.Context) error {
	if s.sessionRouter == nil {
		return nil
	}
	return s.sessionRouter.Apply(ctx)
}

func (s *Service) closeClientSessionConnections(ctx context.Context, listenerID, username string) error {
	if s.sessionRouter == nil {
		return nil
	}
	if err := s.sessionRouter.CloseConnectionsByInboundUser(ctx, listenerID, username); err != nil {
		return fmt.Errorf("close previous residential client connections: %w", err)
	}
	return nil
}

func validateClientSessionID(value string) error {
	if len(value) < 4 || len(value) > 64 {
		return fmt.Errorf("%w: session_id must contain 4 to 64 characters", ErrInvalid)
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') &&
			(character < 'A' || character > 'Z') &&
			(character < '0' || character > '9') && character != '-' && character != '_' {
			return fmt.Errorf("%w: session_id may contain only letters, digits, '-' and '_'", ErrInvalid)
		}
	}
	return nil
}

func clientSessionUsername(channelID, sessionID string) string {
	digest := sha256.Sum256([]byte(channelID + "\x00" + sessionID))
	return "hx-session-" + hex.EncodeToString(digest[:12])
}

func clientSessionAssociatedData(channelID, sessionID string) []byte {
	return []byte("residential-client-session:" + channelID + ":" + sessionID)
}
