package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/HengXin666/HX-ProxyGroup/internal/metrics"
)

type fakeTrafficService struct {
	series   metrics.Series
	items    []metrics.Summary
	queryErr error
}

func (service *fakeTrafficService) Summary(_ context.Context, resourceType, resourceID string) (metrics.Summary, error) {
	return metrics.Summary{ResourceType: resourceType, ResourceID: resourceID}, nil
}

func (service *fakeTrafficService) ListSummaries(context.Context, string, int, int) ([]metrics.Summary, error) {
	return service.items, nil
}

func (service *fakeTrafficService) Query(context.Context, string, string, time.Time, time.Time, int) (metrics.Series, error) {
	return service.series, service.queryErr
}

func TestTrafficAPIListsBoundedSummaries(t *testing.T) {
	traffic := &fakeTrafficService{items: []metrics.Summary{{ResourceType: "node", ResourceID: "node-1", DownloadBytes: 42}}}
	server, err := NewServer(&stubBundleService{}, slog.New(slog.NewTextHandler(io.Discard, nil)), WithTraffic(traffic))
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/traffic?resource_type=node&limit=10", nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var payload struct {
		Items []metrics.Summary `json:"items"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Items) != 1 || payload.Items[0].DownloadBytes != 42 {
		t.Fatalf("payload = %+v", payload)
	}
}

func TestTrafficAPIRedactsRepositoryErrors(t *testing.T) {
	traffic := &fakeTrafficService{queryErr: errors.New("sqlite secret detail")}
	server, err := NewServer(&stubBundleService{}, slog.New(slog.NewTextHandler(io.Discard, nil)), WithTraffic(traffic))
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/traffic?resource_type=node&resource_id=node-1", nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusInternalServerError || strings.Contains(response.Body.String(), "sqlite secret detail") {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestTrafficAPIRejectsInvalidAndOversizedQueries(t *testing.T) {
	server, err := NewServer(&stubBundleService{}, slog.New(slog.NewTextHandler(io.Discard, nil)), WithTraffic(&fakeTrafficService{}))
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{
		"/api/v1/traffic?resource_type=unknown",
		"/api/v1/traffic?resource_type=node&resource_id=node-1&max_points=501",
		"/api/v1/traffic?resource_type=node&resource_id=node-1&from=invalid",
	} {
		request := httptest.NewRequest(http.MethodGet, target, nil)
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, request)
		if response.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s status = %d, body = %s", target, response.Code, response.Body.String())
		}
	}
}
