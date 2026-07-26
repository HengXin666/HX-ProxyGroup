package proxyservice

import (
	"context"
	"errors"
	"testing"

	"github.com/HengXin666/HX-ProxyGroup/internal/listener"
	"github.com/HengXin666/HX-ProxyGroup/internal/proxygroup"
)

type fakeGroups struct {
	created proxygroup.Group
	deleted string
}

func (groups *fakeGroups) Create(context.Context, proxygroup.CreateRequest) (proxygroup.Group, error) {
	groups.created = proxygroup.Group{ID: "group-1", Version: 1}
	return groups.created, nil
}

func (groups *fakeGroups) Delete(_ context.Context, id string, _ int) error {
	groups.deleted = id
	return nil
}

type fakeListeners struct {
	err error
}

func (listeners *fakeListeners) Create(_ context.Context, request listener.CreateRequest) (listener.Listener, error) {
	if listeners.err != nil {
		return listener.Listener{}, listeners.err
	}
	return listener.Listener{ID: "listener-1", ProxyGroupID: request.ProxyGroupID}, nil
}

func TestCreateBindsListenerToGroup(t *testing.T) {
	service, err := NewService(&fakeGroups{}, &fakeListeners{})
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.Create(context.Background(), CreateRequest{Listener: ListenerCreateRequest{Port: 7890}})
	if err != nil {
		t.Fatal(err)
	}
	if created.Listener.ProxyGroupID != created.Group.ID {
		t.Fatalf("listener group = %q, want %q", created.Listener.ProxyGroupID, created.Group.ID)
	}
}

func TestCreateRemovesGroupWhenListenerFails(t *testing.T) {
	groups := &fakeGroups{}
	service, err := NewService(groups, &fakeListeners{err: errors.New("port conflict")})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Create(context.Background(), CreateRequest{})
	if !errors.Is(err, ErrCreateFailed) || groups.deleted != "group-1" {
		t.Fatalf("Create() error = %v, deleted = %q", err, groups.deleted)
	}
}
