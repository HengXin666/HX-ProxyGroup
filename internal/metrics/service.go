package metrics

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/HengXin666/HX-ProxyGroup/internal/store"
)

const MaxQueryRange = 30 * 24 * time.Hour

var ErrInvalidQuery = errors.New("invalid traffic query")

type Resource struct {
	Type string
	ID   string
}

type Connection struct {
	ID        string
	Upload    int64
	Download  int64
	Resources []Resource
}

type RuntimeSnapshot struct {
	Connections []Connection
}

type Source interface {
	TrafficSnapshot(context.Context) (RuntimeSnapshot, error)
}

type Repository interface {
	WriteTraffic(context.Context, []store.TrafficWrite, time.Time) error
	GetTrafficTotal(context.Context, string, string) (store.TrafficTotalRecord, error)
	ListTrafficTotals(context.Context, string, int, int) ([]store.TrafficTotalRecord, error)
	ListTrafficBuckets(context.Context, string, string, time.Time, time.Time, time.Duration, int) ([]store.TrafficBucketRecord, error)
	CompactTraffic(context.Context, time.Time) error
}

type Config struct {
	PollInterval    time.Duration
	FlushInterval   time.Duration
	CompactInterval time.Duration
}

type counters struct {
	upload      int64
	download    int64
	connections int64
}

type observedConnection struct {
	upload   int64
	download int64
}

type Service struct {
	repository Repository
	source     Source
	logger     *slog.Logger
	config     Config
	now        func() time.Time

	mu         sync.Mutex
	observed   map[string]observedConnection
	pending    map[Resource]counters
	active     map[Resource]int64
	lastActive map[Resource]int64
}

func NewService(repository Repository, source Source, logger *slog.Logger, config Config) (*Service, error) {
	if repository == nil {
		return nil, errors.New("traffic repository is required")
	}
	if source == nil {
		return nil, errors.New("traffic source is required")
	}
	if logger == nil {
		logger = slog.Default()
	}
	if config.PollInterval <= 0 {
		config.PollInterval = time.Second
	}
	if config.FlushInterval <= 0 {
		config.FlushInterval = time.Minute
	}
	if config.CompactInterval <= 0 {
		config.CompactInterval = time.Hour
	}
	return &Service{
		repository: repository,
		source:     source,
		logger:     logger,
		config:     config,
		now:        time.Now,
		observed:   make(map[string]observedConnection),
		pending:    make(map[Resource]counters),
		active:     make(map[Resource]int64),
		lastActive: make(map[Resource]int64),
	}, nil
}

func (s *Service) Run(ctx context.Context) error {
	if err := s.repository.CompactTraffic(ctx, s.now().UTC()); err != nil && !errors.Is(err, context.Canceled) {
		s.logger.Error("compact traffic history at startup", "error", err)
	}
	pollTicker := time.NewTicker(s.config.PollInterval)
	flushTicker := time.NewTicker(s.config.FlushInterval)
	compactTicker := time.NewTicker(s.config.CompactInterval)
	defer pollTicker.Stop()
	defer flushTicker.Stop()
	defer compactTicker.Stop()

	var lastCollectWarning time.Time
	for {
		select {
		case <-ctx.Done():
			shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			err := s.Flush(shutdownContext)
			cancel()
			return err
		case <-pollTicker.C:
			if err := s.Collect(ctx); err != nil {
				now := s.now()
				if lastCollectWarning.IsZero() || now.Sub(lastCollectWarning) >= time.Minute {
					s.logger.Warn("collect Mihomo traffic snapshot", "error", err)
					lastCollectWarning = now
				}
			}
		case <-flushTicker.C:
			if err := s.Flush(ctx); err != nil {
				s.logger.Error("flush aggregated traffic", "error", err)
			}
		case <-compactTicker.C:
			if err := s.repository.CompactTraffic(ctx, s.now().UTC()); err != nil {
				s.logger.Error("compact traffic history", "error", err)
			}
		}
	}
}

func (s *Service) Collect(ctx context.Context) error {
	snapshot, err := s.source.TrafficSnapshot(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	nextObserved := make(map[string]observedConnection, len(snapshot.Connections))
	active := make(map[Resource]int64)
	for _, connection := range snapshot.Connections {
		if connection.ID == "" || connection.Upload < 0 || connection.Download < 0 {
			continue
		}
		previous, existed := s.observed[connection.ID]
		uploadDelta := nonnegativeDelta(connection.Upload, previous.upload)
		downloadDelta := nonnegativeDelta(connection.Download, previous.download)
		connectionDelta := int64(0)
		if !existed {
			connectionDelta = 1
		}
		seenResources := make(map[Resource]struct{}, len(connection.Resources))
		for _, resource := range connection.Resources {
			if !validResource(resource) {
				continue
			}
			if _, duplicate := seenResources[resource]; duplicate {
				continue
			}
			seenResources[resource] = struct{}{}
			value := s.pending[resource]
			value.upload += uploadDelta
			value.download += downloadDelta
			value.connections += connectionDelta
			s.pending[resource] = value
			active[resource]++
		}
		nextObserved[connection.ID] = observedConnection{upload: connection.Upload, download: connection.Download}
	}
	s.observed = nextObserved
	s.active = active
	return nil
}

func (s *Service) Flush(ctx context.Context) error {
	s.mu.Lock()
	resources := make(map[Resource]struct{}, len(s.pending)+len(s.active)+len(s.lastActive))
	for resource := range s.pending {
		resources[resource] = struct{}{}
	}
	for resource := range s.active {
		resources[resource] = struct{}{}
	}
	for resource := range s.lastActive {
		resources[resource] = struct{}{}
	}
	now := s.now().UTC()
	writes := make([]store.TrafficWrite, 0, len(resources))
	flushed := make(map[Resource]counters, len(s.pending))
	for resource, value := range s.pending {
		flushed[resource] = value
	}
	flushedActive := make(map[Resource]int64, len(s.active))
	for resource, count := range s.active {
		flushedActive[resource] = count
	}
	for resource := range resources {
		value := s.pending[resource]
		writes = append(writes, store.TrafficWrite{
			ResourceType:      resource.Type,
			ResourceID:        resource.ID,
			BucketStart:       now.Truncate(time.Minute),
			Granularity:       time.Minute,
			UploadBytes:       value.upload,
			DownloadBytes:     value.download,
			ConnectionCount:   value.connections,
			ActiveConnections: s.active[resource],
		})
	}
	s.mu.Unlock()

	sort.Slice(writes, func(left, right int) bool {
		if writes[left].ResourceType == writes[right].ResourceType {
			return writes[left].ResourceID < writes[right].ResourceID
		}
		return writes[left].ResourceType < writes[right].ResourceType
	})
	if err := s.repository.WriteTraffic(ctx, writes, now); err != nil {
		return err
	}

	s.mu.Lock()
	for resource, value := range flushed {
		current := s.pending[resource]
		current.upload -= value.upload
		current.download -= value.download
		current.connections -= value.connections
		if current == (counters{}) {
			delete(s.pending, resource)
		} else {
			s.pending[resource] = current
		}
	}
	s.lastActive = make(map[Resource]int64, len(flushedActive))
	for resource, count := range flushedActive {
		s.lastActive[resource] = count
	}
	s.mu.Unlock()
	return nil
}

type Summary struct {
	ResourceType      string     `json:"resource_type"`
	ResourceID        string     `json:"resource_id"`
	UploadBytes       int64      `json:"upload_bytes"`
	DownloadBytes     int64      `json:"download_bytes"`
	ConnectionCount   int64      `json:"connection_count"`
	ActiveConnections int64      `json:"active_connections"`
	UpdatedAt         *time.Time `json:"updated_at,omitempty"`
}

type Point struct {
	Time                  time.Time `json:"time"`
	UploadBytes           int64     `json:"upload_bytes"`
	DownloadBytes         int64     `json:"download_bytes"`
	ConnectionCount       int64     `json:"connection_count"`
	PeakActiveConnections int64     `json:"peak_active_connections"`
}

type Series struct {
	Summary    Summary   `json:"summary"`
	From       time.Time `json:"from"`
	To         time.Time `json:"to"`
	Resolution int64     `json:"resolution_seconds"`
	Points     []Point   `json:"points"`
}

func (s *Service) Summary(ctx context.Context, resourceType, resourceID string) (Summary, error) {
	record, err := s.repository.GetTrafficTotal(ctx, resourceType, resourceID)
	if err != nil {
		return Summary{}, err
	}
	return summaryFromRecord(record), nil
}

func (s *Service) ListSummaries(ctx context.Context, resourceType string, limit, offset int) ([]Summary, error) {
	records, err := s.repository.ListTrafficTotals(ctx, resourceType, limit, offset)
	if err != nil {
		return nil, err
	}
	items := make([]Summary, 0, len(records))
	for _, record := range records {
		items = append(items, summaryFromRecord(record))
	}
	return items, nil
}

func (s *Service) Query(ctx context.Context, resourceType, resourceID string, from, to time.Time, maxPoints int) (Series, error) {
	if !validResource(Resource{Type: resourceType, ID: resourceID}) {
		return Series{}, fmt.Errorf("%w: invalid traffic resource", ErrInvalidQuery)
	}
	if from.IsZero() || to.IsZero() || !from.Before(to) || to.Sub(from) > MaxQueryRange {
		return Series{}, fmt.Errorf("%w: traffic range must be positive and no longer than %s", ErrInvalidQuery, MaxQueryRange)
	}
	if maxPoints < 1 || maxPoints > 500 {
		return Series{}, fmt.Errorf("%w: max_points must be between 1 and 500", ErrInvalidQuery)
	}
	resolution := chooseResolution(to.Sub(from), maxPoints)
	records, err := s.repository.ListTrafficBuckets(ctx, resourceType, resourceID, from, to, resolution, maxPoints)
	if err != nil {
		return Series{}, err
	}
	total, err := s.Summary(ctx, resourceType, resourceID)
	if err != nil {
		return Series{}, err
	}
	points := make([]Point, 0, len(records))
	for _, record := range records {
		points = append(points, Point{
			Time:                  record.BucketStart,
			UploadBytes:           record.UploadBytes,
			DownloadBytes:         record.DownloadBytes,
			ConnectionCount:       record.ConnectionCount,
			PeakActiveConnections: record.PeakActiveConnections,
		})
	}
	return Series{Summary: total, From: from.UTC(), To: to.UTC(), Resolution: int64(resolution / time.Second), Points: points}, nil
}

func chooseResolution(span time.Duration, maxPoints int) time.Duration {
	if span <= time.Duration(maxPoints)*time.Minute && span <= 24*time.Hour {
		return time.Minute
	}
	if span <= time.Duration(maxPoints)*5*time.Minute && span <= 7*24*time.Hour {
		return 5 * time.Minute
	}
	return time.Hour
}

func summaryFromRecord(record store.TrafficTotalRecord) Summary {
	result := Summary{
		ResourceType:      record.ResourceType,
		ResourceID:        record.ResourceID,
		UploadBytes:       record.UploadBytes,
		DownloadBytes:     record.DownloadBytes,
		ConnectionCount:   record.ConnectionCount,
		ActiveConnections: record.ActiveConnections,
	}
	if !record.UpdatedAt.IsZero() {
		updatedAt := record.UpdatedAt
		result.UpdatedAt = &updatedAt
	}
	return result
}

func nonnegativeDelta(current, previous int64) int64 {
	if current < previous {
		return current
	}
	return current - previous
}

func validResource(resource Resource) bool {
	return resource.ID != "" && (resource.Type == store.TrafficResourceListener ||
		resource.Type == store.TrafficResourceProxyGroup || resource.Type == store.TrafficResourceNode)
}
