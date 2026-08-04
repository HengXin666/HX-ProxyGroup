package residential

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/HengXin666/HX-ProxyGroup/internal/store"
)

// MaxDeclaredSessions bounds how many nodes one channel may publish. It
// matches the database CHECK constraint on session_count.
const MaxDeclaredSessions = 64

// declaredSessionID renders the stable logical id for an ordinal. Node names
// and credentials are keyed on this, so it must never depend on allocation
// state or on the current residential IP.
func declaredSessionID(index int) string {
	return fmt.Sprintf("s%02d", index)
}

// declaredSessionIndex parses a logical id produced by declaredSessionID and
// reports whether the id belongs to the declared set at all.
func declaredSessionIndex(sessionID string) (int, bool) {
	if len(sessionID) < 3 || sessionID[0] != 's' {
		return 0, false
	}
	index := 0
	for _, character := range sessionID[1:] {
		if character < '0' || character > '9' {
			return 0, false
		}
		index = index*10 + int(character-'0')
		if index > MaxDeclaredSessions {
			return 0, false
		}
	}
	if index < 1 {
		return 0, false
	}
	return index, true
}

// DeclaredNodeName is the published node name for one ordinal. Clients see
// this string in their subscription and it stays stable across rotations.
func DeclaredNodeName(channelName string, index int) string {
	return fmt.Sprintf("%s-%02d", strings.TrimSpace(channelName), index)
}

func validateSessionCount(count int, provider Provider) (int, error) {
	if count < 0 || count > MaxDeclaredSessions {
		return 0, fmt.Errorf("%w: session_count must be between 0 and %d", ErrInvalid, MaxDeclaredSessions)
	}
	// Zero preserves the pre-0.2.0 on-demand behaviour for channels that have
	// not opted into a published node list.
	if count == 0 {
		return 0, nil
	}
	if limit := provider.MaxConcurrentSessions; limit > 0 && count > limit {
		return 0, fmt.Errorf(
			"%w: session_count %d exceeds provider concurrent session limit %d",
			ErrInvalid, count, limit,
		)
	}
	return count, nil
}

func validateIdleReleaseSeconds(seconds int) (int, error) {
	if seconds < 0 {
		return 0, fmt.Errorf("%w: idle_release_seconds must not be negative", ErrInvalid)
	}
	// A very short idle window would release an allocation between two requests
	// of the same browser flow and burn provider quota for no benefit.
	if seconds > 0 && seconds < 60 {
		return 0, fmt.Errorf("%w: idle_release_seconds must be 0 or at least 60", ErrInvalid)
	}
	return seconds, nil
}

// SyncDeclaredSessions makes the persisted session set match the channel's
// declared session_count. Growing the count provisions the missing ordinals;
// shrinking it releases them from the tail so the surviving nodes keep their
// names, credentials and residential IPs.
//
// It is safe to call repeatedly: an ordinal that already exists is left alone.
func (s *Service) SyncDeclaredSessions(ctx context.Context, channelID string) error {
	s.clientSessionMutex.Lock()
	defer s.clientSessionMutex.Unlock()
	return s.syncDeclaredSessionsLocked(ctx, channelID)
}

func (s *Service) syncDeclaredSessionsLocked(ctx context.Context, channelID string) error {
	channel, err := s.repository.GetResidentialChannel(ctx, channelID)
	if err != nil {
		return mapStoreError(err)
	}
	if channel.Mode != ModeSticky || channel.SessionCount < 1 {
		return nil
	}
	providerRecord, err := s.repository.GetResidentialProvider(ctx, channel.ProviderID)
	if err != nil {
		return mapStoreError(err)
	}
	existing, err := s.repository.ListResidentialClientSessions(ctx, channel.ID)
	if err != nil {
		return err
	}
	declared := make(map[int]store.ResidentialClientSessionRecord, len(existing))
	for _, session := range existing {
		index, ok := declaredSessionIndex(session.SessionID)
		if !ok {
			continue
		}
		declared[index] = session
	}

	// Shrink first so releasing quota can make room for nothing else to fail
	// on the provider's concurrency limit during a resize in both directions.
	for index, session := range declared {
		if index <= channel.SessionCount {
			continue
		}
		if err := s.deleteClientSession(ctx, channel, session); err != nil {
			return fmt.Errorf("release declared session %s: %w", session.SessionID, err)
		}
		delete(declared, index)
	}

	eager := channel.IdleReleaseSeconds == 0
	for index := 1; index <= channel.SessionCount; index++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, exists := declared[index]; exists {
			continue
		}
		if err := s.createDeclaredSession(ctx, channel, providerRecord, index, eager); err != nil {
			return fmt.Errorf("provision declared session %s: %w", declaredSessionID(index), err)
		}
	}
	return nil
}

// createDeclaredSession provisions one ordinal. When eager is false the
// session is created with credentials but no residential allocation, so a
// published subscription still lists the node and the first real request
// triggers allocation.
func (s *Service) createDeclaredSession(
	ctx context.Context,
	channel store.ResidentialChannelRecord,
	providerRecord store.ResidentialProviderRecord,
	index int,
	eager bool,
) error {
	sessionID := declaredSessionID(index)
	password, err := s.newClientSessionPassword(ctx, channel.ListenerID)
	if err != nil {
		return err
	}
	encrypted, err := s.cipher.Seal([]byte(password), clientSessionAssociatedData(channel.ID, sessionID))
	if err != nil {
		return fmt.Errorf("encrypt declared session password: %w", err)
	}
	countryCode, err := clientSessionCountry(channel, "")
	if err != nil {
		return err
	}
	now := s.now().UTC()
	record := store.ResidentialClientSessionRecord{
		ChannelID:             channel.ID,
		SessionID:             sessionID,
		AuthUsername:          clientSessionUsername(channel.ID, sessionID),
		AuthPasswordEncrypted: encrypted,
		SessionIndex:          -1,
		DeclaredIndex:         index,
		RouteMode:             ClientRouteResidential,
		CountryCode:           countryCode,
		CreatedAt:             now,
		UpdatedAt:             now,
	}
	if eager {
		fingerprint, expiresAt, err := s.allocateClientSessionNode(
			ctx, channel, providerRecord, sessionID, now, countryCode,
		)
		if err != nil {
			return err
		}
		record.NodeFingerprint = fingerprint
		record.AllocatedAt = &now
		record.ExpiresAt = expiresAt
	}
	created, err := s.repository.CreateResidentialClientSession(ctx, record)
	if err != nil {
		if record.NodeFingerprint != "" {
			_ = s.repository.DeleteResidentialSessionNode(ctx, channel.ID, record.NodeFingerprint)
		}
		return mapStoreError(err)
	}
	if err := s.republishClientSessionGroup(ctx, channel); err != nil {
		_ = s.repository.DeleteResidentialClientSession(ctx, channel.ID, created.SessionID)
		if record.NodeFingerprint != "" {
			_ = s.repository.DeleteResidentialSessionNode(ctx, channel.ID, record.NodeFingerprint)
		}
		_ = s.republishClientSessionGroup(ctx, channel)
		return fmt.Errorf("publish declared session: %w", err)
	}
	return nil
}

// declaredSessionViews renders the published node list for one channel in
// ordinal order. Every declared ordinal appears even when it currently has no
// residential allocation, because the node name is what a client subscribed to.
//
// ExitIP is left empty: a residential node record carries the vendor gateway
// address, not the egress address, so reporting one would require a probe per
// session on every channel read.
func (s *Service) declaredSessionViews(
	ctx context.Context,
	channel store.ResidentialChannelRecord,
) ([]ChannelSession, error) {
	if channel.SessionCount < 1 {
		return nil, nil
	}
	sessions, err := s.repository.ListResidentialClientSessions(ctx, channel.ID)
	if err != nil {
		return nil, err
	}
	byIndex := make(map[int]store.ResidentialClientSessionRecord, len(sessions))
	for _, session := range sessions {
		if index, ok := declaredSessionIndex(session.SessionID); ok {
			byIndex[index] = session
		}
	}
	views := make([]ChannelSession, 0, channel.SessionCount)
	for index := 1; index <= channel.SessionCount; index++ {
		view := ChannelSession{
			Index:     index,
			SessionID: declaredSessionID(index),
			NodeName:  DeclaredNodeName(channel.Name, index),
			RouteMode: ClientRouteResidential,
		}
		if session, exists := byIndex[index]; exists {
			view.RouteMode = session.RouteMode
			view.CountryCode = session.CountryCode
			view.Allocated = session.NodeFingerprint != ""
			view.AllocatedAt = session.AllocatedAt
			view.ExpiresAt = session.ExpiresAt
			view.RotateCount = session.RotateCount
			view.LastRotatedAt = session.LastRotatedAt
			view.LastUsedAt = session.LastUsedAt
		}
		views = append(views, view)
	}
	return views, nil
}

// ReleaseIdleDeclaredSessions returns residential allocations whose sessions
// have been unused for longer than the channel's idle window. Node names and
// credentials survive, so a published subscription stays valid and the next
// request reallocates transparently.
//
// Work is bounded by limit and serialized with allocation and rotation.
func (s *Service) ReleaseIdleDeclaredSessions(ctx context.Context, limit int) (int, error) {
	if limit < 1 {
		return 0, nil
	}
	s.clientSessionMutex.Lock()
	defer s.clientSessionMutex.Unlock()
	channels, err := s.repository.ListResidentialChannels(ctx)
	if err != nil {
		return 0, err
	}
	released := 0
	now := s.now().UTC()
	for _, channel := range channels {
		if released >= limit {
			break
		}
		if channel.Mode != ModeSticky || channel.IdleReleaseSeconds <= 0 {
			continue
		}
		idleWindow := time.Duration(channel.IdleReleaseSeconds) * time.Second
		sessions, err := s.repository.ListResidentialClientSessions(ctx, channel.ID)
		if err != nil {
			return released, err
		}
		for _, session := range sessions {
			if released >= limit {
				break
			}
			if err := ctx.Err(); err != nil {
				return released, err
			}
			if !declaredSessionIdle(session, now, idleWindow) {
				continue
			}
			if err := s.releaseDeclaredAllocation(ctx, channel, session); err != nil {
				return released, err
			}
			released++
		}
	}
	return released, nil
}

func declaredSessionIdle(
	session store.ResidentialClientSessionRecord,
	now time.Time,
	idleWindow time.Duration,
) bool {
	if _, ok := declaredSessionIndex(session.SessionID); !ok {
		return false
	}
	if session.RouteMode != ClientRouteResidential || session.NodeFingerprint == "" {
		return false
	}
	// A session that has never been used still counts from its allocation
	// time, otherwise an unused eager allocation would be held forever.
	reference := session.LastUsedAt
	if reference == nil {
		reference = session.AllocatedAt
	}
	if reference == nil {
		return false
	}
	return now.Sub(reference.UTC()) >= idleWindow
}

// releaseDeclaredAllocation drops only the residential node. The session row,
// its ordinal and its credentials are preserved.
func (s *Service) releaseDeclaredAllocation(
	ctx context.Context,
	channel store.ResidentialChannelRecord,
	session store.ResidentialClientSessionRecord,
) error {
	updated, err := s.repository.ClearResidentialClientSessionAllocation(
		ctx, channel.ID, session.SessionID,
	)
	if err != nil {
		return mapStoreError(err)
	}
	if err := s.repository.DeleteResidentialSessionNode(ctx, channel.ID, session.NodeFingerprint); err != nil {
		return err
	}
	if err := s.republishClientSessionGroup(ctx, channel); err != nil {
		return fmt.Errorf("release idle residential allocation: %w", err)
	}
	return s.closeChannelClientConnections(ctx, channel, updated.AuthUsername)
}
