package proxyservice

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/HengXin666/HX-ProxyGroup/internal/listener"
	"github.com/HengXin666/HX-ProxyGroup/internal/proxygroup"
	"github.com/HengXin666/HX-ProxyGroup/internal/store"
	"github.com/HengXin666/HX-ProxyGroup/internal/systemsettings"
)

type fakeGroups struct {
	created     proxygroup.Group
	deleted     string
	stored      proxygroup.Group
	updateCalls []proxygroup.UpdateRequest
}

func (groups *fakeGroups) Create(context.Context, proxygroup.CreateRequest) (proxygroup.Group, error) {
	groups.created = proxygroup.Group{ID: "group-1", Version: 1}
	return groups.created, nil
}

func (groups *fakeGroups) Get(context.Context, string) (proxygroup.Group, error) {
	if groups.stored.ID == "" {
		groups.stored = proxygroup.Group{ID: "group-1", Version: 1, Name: "original"}
	}
	return groups.stored, nil
}

func (groups *fakeGroups) Update(_ context.Context, _ string, request proxygroup.UpdateRequest) (proxygroup.Group, error) {
	groups.updateCalls = append(groups.updateCalls, request)
	return proxygroup.Group{
		ID:               "group-1",
		Name:             request.Name,
		Strategy:         request.Strategy,
		SourceSpec:       request.SourceSpec,
		Enabled:          request.Enabled,
		EmptyBehavior:    request.EmptyBehavior,
		FallbackTargetID: request.FallbackTarget,
		Version:          request.Version + 1,
	}, nil
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

func (listeners *fakeListeners) EnsureSharedInbounds(context.Context, listener.SharedInboundSpec, []store.ProxyGroupRecord, []listener.SharedInboundMember) ([]listener.Listener, error) {
	return nil, nil
}

func (listeners *fakeListeners) Update(_ context.Context, _ string, request listener.UpdateRequest) (listener.Listener, error) {
	if listeners.err != nil {
		return listener.Listener{}, listeners.err
	}
	return listener.Listener{ID: "listener-1", ProxyGroupID: request.ProxyGroupID, Name: request.Name}, nil
}

func TestCreateBindsListenerToGroup(t *testing.T) {
	service, err := NewService(&fakeGroups{}, &fakeListeners{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.Create(context.Background(), CreateRequest{Listener: ListenerCreateRequest{
		Port: 7890,
		Auth: &listener.Auth{Username: "hx-user", Password: "hx-pass"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if created.Listener.ProxyGroupID != created.Group.ID {
		t.Fatalf("listener group = %q, want %q", created.Listener.ProxyGroupID, created.Group.ID)
	}
}

// TestCreateRejectsSharedMemberWithoutCredentials pins the shared-inbound rule
// that a member must be selectable by username: without credentials Mihomo
// would accept every connection on the aggregate port.
func TestCreateRejectsSharedMemberWithoutCredentials(t *testing.T) {
	service, err := NewService(&fakeGroups{}, &fakeListeners{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.Create(context.Background(), CreateRequest{Listener: ListenerCreateRequest{Port: 7890}}); err == nil {
		t.Fatal("Create() error = nil, want a credential requirement")
	}
}

// TestCreateUsesDedicatedPortWhenSharedInboundDisabled keeps the historical
// per-port behaviour working after an upgrade.
func TestCreateUsesDedicatedPortWhenSharedInboundDisabled(t *testing.T) {
	settings := &fakeSettings{mode: systemsettings.SharedInboundPerService}
	service, err := NewService(&fakeGroups{}, &fakeListeners{}, settings)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.Create(context.Background(), CreateRequest{Listener: ListenerCreateRequest{
		Kind: "mixed", BindAddress: "127.0.0.1", Port: 7890,
	}}); err != nil {
		t.Fatal(err)
	}
}

type fakeSettings struct {
	mode systemsettings.SharedInboundMode
}

func (settings *fakeSettings) Get(context.Context) (systemsettings.Settings, error) {
	current := systemsettings.Default()
	current.SharedInbound.Mode = settings.mode
	return current, nil
}

func TestCreateRemovesGroupWhenListenerFails(t *testing.T) {
	groups := &fakeGroups{}
	service, err := NewService(groups, &fakeListeners{err: errors.New("port conflict")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Create(context.Background(), CreateRequest{})
	if !errors.Is(err, ErrCreateFailed) || groups.deleted != "group-1" {
		t.Fatalf("Create() error = %v, deleted = %q", err, groups.deleted)
	}
}

func TestUpdateBindsUpdatedListenerToGroup(t *testing.T) {
	groups := &fakeGroups{stored: proxygroup.Group{ID: "group-1", Version: 1, Name: "original"}}
	service, err := NewService(groups, &fakeListeners{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := service.Update(context.Background(), UpdateRequest{
		GroupID:         "group-1",
		GroupVersion:    1,
		Name:            "renamed",
		ListenerID:      "listener-1",
		ListenerVersion: 1,
		Listener:        ListenerUpdateRequest{Name: "listener-renamed"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Group.Name != "renamed" {
		t.Fatalf("group name = %q, want %q", updated.Group.Name, "renamed")
	}
	if updated.Listener.ProxyGroupID != "group-1" {
		t.Fatalf("listener group = %q, want %q", updated.Listener.ProxyGroupID, "group-1")
	}
	if updated.Listener.Name != "listener-renamed" {
		t.Fatalf("listener name = %q, want %q", updated.Listener.Name, "listener-renamed")
	}
}

func TestUpdateRollsBackGroupWhenListenerFails(t *testing.T) {
	groups := &fakeGroups{stored: proxygroup.Group{ID: "group-1", Version: 1, Name: "original"}}
	service, err := NewService(groups, &fakeListeners{err: errors.New("port conflict")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Update(context.Background(), UpdateRequest{
		GroupID:         "group-1",
		GroupVersion:    1,
		Name:            "renamed",
		ListenerID:      "listener-1",
		ListenerVersion: 1,
		Listener:        ListenerUpdateRequest{Name: "listener-renamed"},
	})
	if err == nil {
		t.Fatal("Update() error = nil, want listener error")
	}
	if len(groups.updateCalls) != 2 {
		t.Fatalf("group update calls = %d, want 2", len(groups.updateCalls))
	}
	rollback := groups.updateCalls[1]
	if rollback.Name != "original" {
		t.Fatalf("rollback name = %q, want %q", rollback.Name, "original")
	}
	firstVersion := groups.updateCalls[0].Version
	if rollback.Version != firstVersion+1 {
		t.Fatalf("rollback version = %d, want %d (version after first group update)", rollback.Version, firstVersion+1)
	}
	if !strings.Contains(err.Error(), "port conflict") {
		t.Fatalf("Update() error = %v, want listener error", err)
	}
}
