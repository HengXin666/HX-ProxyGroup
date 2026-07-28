package api

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/HengXin666/HX-ProxyGroup/internal/dataplane/mihomo"
	"github.com/HengXin666/HX-ProxyGroup/internal/overview"
)

type fakeOverviewService struct {
	mu    sync.Mutex
	count int
}

func (service *fakeOverviewService) Status() mihomo.Status {
	return mihomo.Status{Running: true}
}

func (service *fakeOverviewService) OverviewSnapshot(context.Context) (overview.Snapshot, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	service.count++
	return overview.Snapshot{Connections: []overview.Connection{{
		ID: "connection-1", Upload: int64(service.count * 100), Download: int64(service.count * 200),
	}}}, nil
}

func TestOverviewStreamEmitsIdleFrameAndSecondLevelSamples(t *testing.T) {
	service := &fakeOverviewService{}
	server, err := NewServer(&stubBundleService{}, slog.New(slog.NewTextHandler(io.Discard, nil)), WithOverview(service))
	if err != nil {
		t.Fatal(err)
	}
	server.overviewInterval = 10 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 28*time.Millisecond)
	defer cancel()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/overview/stream", nil).WithContext(ctx)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)

	if recorder.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("Content-Type = %q", recorder.Header().Get("Content-Type"))
	}
	body := recorder.Body.String()
	if count := strings.Count(body, "event: sample"); count < 2 {
		t.Fatalf("sample count = %d, body = %q", count, body)
	}
	if !strings.Contains(body, `"upload_bytes_per_second":0`) {
		t.Fatalf("initial idle sample missing: %q", body)
	}
}

func TestOverviewStreamRejectsWrites(t *testing.T) {
	server, err := NewServer(&stubBundleService{}, slog.Default(), WithOverview(&fakeOverviewService{}))
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/overview/stream", nil)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusMethodNotAllowed)
	}
}
