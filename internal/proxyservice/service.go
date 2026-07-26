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
	Delete(context.Context, string, int) error
}

type ListenerService interface {
	Create(context.Context, listener.CreateRequest) (listener.Listener, error)
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
	Name        string         `json:"name"`
	Kind        string         `json:"kind"`
	BindAddress string         `json:"bind_address"`
	Port        int            `json:"port"`
	Auth        *listener.Auth `json:"auth,omitempty"`
	Enabled     *bool          `json:"enabled,omitempty"`
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
		Name:         request.Listener.Name,
		Kind:         request.Listener.Kind,
		BindAddress:  request.Listener.BindAddress,
		Port:         request.Listener.Port,
		ProxyGroupID: group.ID,
		Auth:         request.Listener.Auth,
		Enabled:      request.Listener.Enabled,
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
