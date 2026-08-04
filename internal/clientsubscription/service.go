// Package clientsubscription publishes all client-facing proxy services as one
// token-addressed subscription without exposing upstream node credentials.
package clientsubscription

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/HengXin666/HX-ProxyGroup/internal/listener"
	"github.com/HengXin666/HX-ProxyGroup/internal/store"
)

const catalogTokenMetadataKey = "client_subscription_token"

type Repository interface {
	GetMetadata(context.Context, string) (string, error)
	SetMetadata(context.Context, string, string) error
}

type ListenerSource interface {
	List(context.Context) ([]listener.Listener, error)
	ExportByID(context.Context, string, string) (listener.ShareExport, error)
}

// Contributor supplies service-specific exports and the listener ids it owns.
// Owned ids are excluded from the plain listener pass so internal residential
// provisioning credentials can never leak into the catalog.
type Contributor interface {
	CatalogEntries(context.Context, string) ([]listener.ShareExport, []string, error)
}

type Info struct {
	SharePath string `json:"share_path"`
	NodeCount int    `json:"node_count"`
}

type Service struct {
	repository  Repository
	listeners   ListenerSource
	contributor Contributor
	mutex       sync.Mutex
}

func NewService(repository Repository, listeners ListenerSource, contributor Contributor) (*Service, error) {
	if repository == nil || listeners == nil {
		return nil, errors.New("client subscription repository and listener source are required")
	}
	return &Service{repository: repository, listeners: listeners, contributor: contributor}, nil
}

func (s *Service) Info(ctx context.Context, requestHost string) (Info, error) {
	token, err := s.token(ctx)
	if err != nil {
		return Info{}, err
	}
	bundle, err := s.build(ctx, requestHost)
	if err != nil {
		return Info{}, err
	}
	return Info{SharePath: "/sub/" + token, NodeCount: bundle.NodeCount()}, nil
}

func (s *Service) Rotate(ctx context.Context, requestHost string) (Info, error) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	bundle, err := s.build(ctx, requestHost)
	if err != nil {
		return Info{}, err
	}
	token, err := newToken()
	if err != nil {
		return Info{}, err
	}
	if err := s.repository.SetMetadata(ctx, catalogTokenMetadataKey, token); err != nil {
		return Info{}, err
	}
	return Info{SharePath: "/sub/" + token, NodeCount: bundle.NodeCount()}, nil
}

// ExportByToken reports whether token belongs to the unified catalog. A false
// match lets the API continue resolving residential and legacy listener links.
func (s *Service) ExportByToken(
	ctx context.Context,
	token, requestHost string,
) (listener.ShareBundle, bool, error) {
	token = strings.TrimSpace(token)
	if len(token) != 32 {
		return listener.ShareBundle{}, false, nil
	}
	current, err := s.token(ctx)
	if err != nil {
		return listener.ShareBundle{}, false, err
	}
	if subtle.ConstantTimeCompare([]byte(current), []byte(token)) != 1 {
		return listener.ShareBundle{}, false, nil
	}
	bundle, err := s.build(ctx, requestHost)
	return bundle, true, err
}

func (s *Service) build(ctx context.Context, requestHost string) (listener.ShareBundle, error) {
	exports := make([]listener.ShareExport, 0)
	owned := make(map[string]struct{})
	if s.contributor != nil {
		contributed, listenerIDs, err := s.contributor.CatalogEntries(ctx, requestHost)
		if err != nil {
			return listener.ShareBundle{}, err
		}
		exports = append(exports, contributed...)
		for _, id := range listenerIDs {
			owned[id] = struct{}{}
		}
	}
	items, err := s.listeners.List(ctx)
	if err != nil {
		return listener.ShareBundle{}, err
	}
	for _, item := range items {
		if !item.Enabled {
			continue
		}
		if _, exists := owned[item.ID]; exists {
			continue
		}
		export, err := s.listeners.ExportByID(ctx, item.ID, requestHost)
		if errors.Is(err, listener.ErrShareDisabled) {
			continue
		}
		if err != nil {
			return listener.ShareBundle{}, fmt.Errorf("export listener %q: %w", item.Name, err)
		}
		exports = append(exports, export)
	}
	return listener.NewShareBundle("HX-ProxyGroup", exports), nil
}

func (s *Service) token(ctx context.Context) (string, error) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	return s.tokenLocked(ctx)
}

func (s *Service) tokenLocked(ctx context.Context) (string, error) {
	token, err := s.repository.GetMetadata(ctx, catalogTokenMetadataKey)
	if err == nil && len(token) == 32 {
		return token, nil
	}
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return "", err
	}
	token, err = newToken()
	if err != nil {
		return "", err
	}
	if err := s.repository.SetMetadata(ctx, catalogTokenMetadataKey, token); err != nil {
		return "", err
	}
	return token, nil
}

func newToken() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate client subscription token: %w", err)
	}
	return hex.EncodeToString(value[:]), nil
}
