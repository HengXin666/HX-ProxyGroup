package proxyservice

import (
	"context"
	"errors"
	"fmt"

	"github.com/HengXin666/HX-ProxyGroup/internal/listener"
	"github.com/HengXin666/HX-ProxyGroup/internal/proxygroup"
)

var ErrCreateFailed = errors.New("create proxy service failed")

type GroupService interface {
	Create(context.Context, proxygroup.CreateRequest) (proxygroup.Group, error)
	Get(context.Context, string) (proxygroup.Group, error)
	Update(context.Context, string, proxygroup.UpdateRequest) (proxygroup.Group, error)
	Delete(context.Context, string, int) error
}

type ListenerService interface {
	Create(context.Context, listener.CreateRequest) (listener.Listener, error)
	Update(context.Context, string, listener.UpdateRequest) (listener.Listener, error)
}

type CreateRequest struct {
	Name          string                `json:"name"`
	Strategy      string                `json:"strategy"`
	SourceSpec    proxygroup.SourceSpec `json:"source_spec"`
	EmptyBehavior string                `json:"empty_behavior,omitempty"`
	Enabled       *bool                 `json:"enabled,omitempty"`
	Listener      ListenerCreateRequest `json:"listener"`
}

type ListenerCreateRequest struct {
	Name           string                  `json:"name"`
	Kind           string                  `json:"kind"`
	BindAddress    string                  `json:"bind_address"`
	Port           int                     `json:"port"`
	Auth           *listener.Auth          `json:"auth,omitempty"`
	Transport      listener.Transport      `json:"transport,omitempty"`
	PublicEndpoint listener.PublicEndpoint `json:"public_endpoint,omitempty"`
	Enabled        *bool                   `json:"enabled,omitempty"`
}

type ListenerUpdateRequest struct {
	Name           string                  `json:"name"`
	Kind           string                  `json:"kind"`
	BindAddress    string                  `json:"bind_address"`
	Port           int                     `json:"port"`
	Auth           *listener.Auth          `json:"auth,omitempty"`
	ClearAuth      bool                    `json:"clear_auth,omitempty"`
	Transport      listener.Transport      `json:"transport,omitempty"`
	PublicEndpoint listener.PublicEndpoint `json:"public_endpoint,omitempty"`
	Enabled        *bool                   `json:"enabled,omitempty"`
}

type UpdateRequest struct {
	GroupID         string                `json:"group_id"`
	GroupVersion    int                   `json:"group_version"`
	Name            string                `json:"name"`
	Strategy        string                `json:"strategy"`
	SourceSpec      proxygroup.SourceSpec `json:"source_spec"`
	Enabled         *bool                 `json:"enabled,omitempty"`
	EmptyBehavior   string                `json:"empty_behavior,omitempty"`
	FallbackTarget  string                `json:"fallback_target_id,omitempty"`
	ListenerID      string                `json:"listener_id"`
	ListenerVersion int                   `json:"listener_version"`
	Listener        ListenerUpdateRequest `json:"listener"`
}

type ServiceRecord struct {
	Group    proxygroup.Group  `json:"group"`
	Listener listener.Listener `json:"listener"`
}

type Service struct {
	groups    GroupService
	listeners ListenerService
}

func NewService(groups GroupService, listeners ListenerService) (*Service, error) {
	if groups == nil || listeners == nil {
		return nil, errors.New("proxy group and listener services are required")
	}
	return &Service{groups: groups, listeners: listeners}, nil
}

func (s *Service) Create(ctx context.Context, request CreateRequest) (ServiceRecord, error) {
	group, err := s.groups.Create(ctx, proxygroup.CreateRequest{
		Name:          request.Name,
		Strategy:      request.Strategy,
		SourceSpec:    request.SourceSpec,
		Enabled:       request.Enabled,
		EmptyBehavior: request.EmptyBehavior,
	})
	if err != nil {
		if group.ID != "" {
			_ = s.groups.Delete(ctx, group.ID, group.Version)
		}
		return ServiceRecord{}, fmt.Errorf("%w: create proxy group: %v", ErrCreateFailed, err)
	}
	createdListener, err := s.listeners.Create(ctx, listener.CreateRequest{
		Name:           request.Listener.Name,
		Kind:           request.Listener.Kind,
		BindAddress:    request.Listener.BindAddress,
		Port:           request.Listener.Port,
		ProxyGroupID:   group.ID,
		Auth:           request.Listener.Auth,
		Transport:      request.Listener.Transport,
		PublicEndpoint: request.Listener.PublicEndpoint,
		Enabled:        request.Listener.Enabled,
	})
	if err != nil {
		cleanupErr := s.groups.Delete(ctx, group.ID, group.Version)
		if cleanupErr != nil {
			return ServiceRecord{Group: group}, fmt.Errorf("%w: create listener: %v; cleanup proxy group: %v", ErrCreateFailed, err, cleanupErr)
		}
		return ServiceRecord{}, fmt.Errorf("%w: create listener: %v", ErrCreateFailed, err)
	}
	return ServiceRecord{Group: group, Listener: createdListener}, nil
}

func (s *Service) Update(ctx context.Context, request UpdateRequest) (ServiceRecord, error) {
	original, err := s.groups.Get(ctx, request.GroupID)
	if err != nil {
		return ServiceRecord{}, err
	}
	groupEnabled := original.Enabled
	if request.Enabled != nil {
		groupEnabled = *request.Enabled
	}
	updatedGroup, err := s.groups.Update(ctx, request.GroupID, proxygroup.UpdateRequest{
		Version:        request.GroupVersion,
		Name:           request.Name,
		Strategy:       request.Strategy,
		SourceSpec:     request.SourceSpec,
		Enabled:        groupEnabled,
		EmptyBehavior:  request.EmptyBehavior,
		FallbackTarget: request.FallbackTarget,
	})
	if err != nil {
		return ServiceRecord{}, err
	}
	listenerEnabled := true
	if request.Listener.Enabled != nil {
		listenerEnabled = *request.Listener.Enabled
	}
	updatedListener, err := s.listeners.Update(ctx, request.ListenerID, listener.UpdateRequest{
		Version:        request.ListenerVersion,
		Name:           request.Listener.Name,
		Kind:           request.Listener.Kind,
		BindAddress:    request.Listener.BindAddress,
		Port:           request.Listener.Port,
		ProxyGroupID:   request.GroupID,
		Auth:           request.Listener.Auth,
		Transport:      request.Listener.Transport,
		PublicEndpoint: request.Listener.PublicEndpoint,
		ClearAuth:      request.Listener.ClearAuth,
		Enabled:        listenerEnabled,
	})
	if err != nil {
		// The listener service already restored its own database record, so
		// this rollback apply compiles from a consistent state. Join any
		// rollback failure into the returned error instead of swallowing it.
		if _, rollbackErr := s.groups.Update(ctx, request.GroupID, proxygroup.UpdateRequest{
			Version:        updatedGroup.Version,
			Name:           original.Name,
			Strategy:       original.Strategy,
			SourceSpec:     original.SourceSpec,
			Enabled:        original.Enabled,
			EmptyBehavior:  original.EmptyBehavior,
			FallbackTarget: original.FallbackTargetID,
		}); rollbackErr != nil {
			return ServiceRecord{}, errors.Join(err, fmt.Errorf("restore proxy group after failed listener update: %w", rollbackErr))
		}
		return ServiceRecord{}, err
	}
	return ServiceRecord{Group: updatedGroup, Listener: updatedListener}, nil
}
