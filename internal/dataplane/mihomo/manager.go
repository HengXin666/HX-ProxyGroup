package mihomo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

var ErrUnavailable = errors.New("mihomo binary is unavailable")

type Status struct {
	Available       bool       `json:"available"`
	Running         bool       `json:"running"`
	State           string     `json:"state"`
	PID             int        `json:"pid,omitempty"`
	Binary          string     `json:"binary,omitempty"`
	Version         string     `json:"version,omitempty"`
	ActiveConfig    string     `json:"active_config,omitempty"`
	LastApplyAt     *time.Time `json:"last_apply_at,omitempty"`
	LastError       string     `json:"last_error,omitempty"`
	ListenerCount   int        `json:"listener_count"`
	ProxyCount      int        `json:"proxy_count"`
	ActiveListeners []Endpoint `json:"active_listeners"`
	EgressInterface string     `json:"egress_interface,omitempty"`
	MaxProcs        int        `json:"max_procs"`
	LogMaxBytes     int64      `json:"log_max_bytes"`
}

type process struct {
	command *exec.Cmd
	done    chan error
}

type Manager struct {
	compiler         *Compiler
	binary           string
	runtimeDir       string
	activePath       string
	candidate        string
	previous         string
	logPath          string
	controllerSocket string
	logger           *slog.Logger
	available        bool
	version          string
	mu               sync.Mutex
	process          *process
	status           Status
	lastCompiled     Compiled
	egressInterface  string
	maxProcs         int
	logMaxBytes      int64
	logBackups       int
	externalProcess  bool
}

type ManagerOption func(*Manager) error

func WithEgressInterface(name string) ManagerOption {
	return func(manager *Manager) error {
		manager.compiler.setEgressInterface(name)
		manager.egressInterface = strings.TrimSpace(name)
		return nil
	}
}

func WithProcessMaxProcs(value int) ManagerOption {
	return func(manager *Manager) error {
		if value < 1 {
			return errors.New("mihomo max procs must be positive")
		}
		manager.maxProcs = value
		return nil
	}
}

func WithLogRotation(maxBytes int64, backups int) ManagerOption {
	return func(manager *Manager) error {
		if maxBytes < 1 || backups < 0 {
			return errors.New("invalid Mihomo log rotation settings")
		}
		manager.logMaxBytes = maxBytes
		manager.logBackups = backups
		return nil
	}
}

func WithExternalProcess(enabled bool) ManagerOption {
	return func(manager *Manager) error {
		manager.externalProcess = enabled
		return nil
	}
}

func NewManager(
	compiler *Compiler,
	binary string,
	activeConfigPath string,
	logger *slog.Logger,
	options ...ManagerOption,
) (*Manager, error) {
	if compiler == nil {
		return nil, errors.New("mihomo compiler is required")
	}
	if strings.TrimSpace(activeConfigPath) == "" {
		return nil, errors.New("mihomo active config path is required")
	}
	if logger == nil {
		logger = slog.Default()
	}
	activePath, err := filepath.Abs(activeConfigPath)
	if err != nil {
		return nil, fmt.Errorf("resolve mihomo active config path: %w", err)
	}
	runtimeDirectory := filepath.Dir(activePath)
	manager := &Manager{
		compiler:         compiler,
		runtimeDir:       runtimeDirectory,
		activePath:       activePath,
		candidate:        activePath + ".candidate",
		previous:         activePath + ".previous",
		logPath:          filepath.Join(runtimeDirectory, "mihomo.log"),
		controllerSocket: filepath.Join(runtimeDirectory, "mihomo.sock"),
		logger:           logger,
		maxProcs:         min(runtime.NumCPU(), 4),
		logMaxBytes:      8 << 20,
		logBackups:       2,
	}
	for _, option := range options {
		if option == nil {
			continue
		}
		if err := option(manager); err != nil {
			return nil, err
		}
	}
	manager.compiler.setControllerSocket(manager.controllerSocket)
	manager.resolveBinary(binary)
	manager.status = Status{
		Available:       manager.available,
		State:           "idle",
		Binary:          manager.binary,
		Version:         manager.version,
		ActiveConfig:    manager.activePath,
		EgressInterface: manager.egressInterface,
		MaxProcs:        manager.maxProcs,
		LogMaxBytes:     manager.logMaxBytes,
	}
	return manager, nil
}

func (m *Manager) Apply(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.refreshProcessLocked()

	compiled, err := m.compiler.Compile(ctx)
	if err != nil {
		m.recordFailureLocked(err)
		return err
	}
	if m.status.Running && bytes.Equal(compiled.YAML, m.lastCompiled.YAML) {
		m.status.ListenerCount = len(compiled.Endpoints)
		m.status.ProxyCount = compiled.ProxyCount
		m.status.ActiveListeners = append([]Endpoint(nil), compiled.Endpoints...)
		m.status.LastError = ""
		return nil
	}
	if !m.externalProcess && len(compiled.Endpoints) == 0 && compiled.ProxyCount == 0 {
		if err := m.stopLocked(ctx); err != nil {
			m.recordFailureLocked(err)
			return err
		}
		now := time.Now().UTC()
		m.lastCompiled = compiled
		m.status.State = "idle"
		m.status.Running = false
		m.status.PID = 0
		m.status.ListenerCount = 0
		m.status.ProxyCount = 0
		m.status.ActiveListeners = nil
		m.status.LastApplyAt = &now
		m.status.LastError = ""
		return nil
	}
	if !m.available {
		err := fmt.Errorf("%w: configure --mihomo or install mihomo", ErrUnavailable)
		m.recordFailureLocked(err)
		return err
	}
	if err := os.MkdirAll(m.runtimeDir, 0o700); err != nil {
		return m.failLocked(fmt.Errorf("create mihomo runtime directory: %w", err))
	}
	if err := os.Chmod(m.runtimeDir, 0o700); err != nil {
		return m.failLocked(fmt.Errorf("secure mihomo runtime directory: %w", err))
	}
	if err := writeFileAtomic(m.candidate, compiled.YAML, 0o600); err != nil {
		return m.failLocked(fmt.Errorf("write mihomo candidate config: %w", err))
	}
	if err := m.validateLocked(ctx, m.candidate); err != nil {
		_ = os.Remove(m.candidate)
		return m.failLocked(err)
	}

	oldEndpoints := append([]Endpoint(nil), m.lastCompiled.Endpoints...)
	hadOldConfig := fileExists(m.activePath)
	if hadOldConfig {
		if err := copyFileAtomic(m.activePath, m.previous, 0o600); err != nil {
			return m.failLocked(fmt.Errorf("preserve previous mihomo config: %w", err))
		}
	}
	if err := publishCandidate(m.candidate, m.activePath); err != nil {
		return m.failLocked(fmt.Errorf("publish mihomo config: %w", err))
	}
	if m.externalProcess {
		if err := m.reloadExternalLocked(ctx); err != nil {
			rollbackErr := m.rollbackAndRestartLocked(ctx, hadOldConfig, oldEndpoints)
			return m.failLocked(errors.Join(err, rollbackErr))
		}
		if err := m.waitReadyLocked(ctx, compiled.Endpoints, compiled.ProxyCount); err != nil {
			rollbackErr := m.rollbackAndRestartLocked(ctx, hadOldConfig, oldEndpoints)
			return m.failLocked(errors.Join(err, rollbackErr))
		}
		now := time.Now().UTC()
		m.lastCompiled = compiled
		m.status.State = "running"
		m.status.Running = true
		m.status.PID = 0
		m.status.ListenerCount = len(compiled.Endpoints)
		m.status.ProxyCount = compiled.ProxyCount
		m.status.ActiveListeners = append([]Endpoint(nil), compiled.Endpoints...)
		m.status.LastApplyAt = &now
		m.status.LastError = ""
		return nil
	}
	if err := m.stopLocked(ctx); err != nil {
		_ = m.rollbackConfigLocked(hadOldConfig)
		return m.failLocked(err)
	}
	if err := m.startLocked(m.activePath); err != nil {
		rollbackErr := m.rollbackAndRestartLocked(ctx, hadOldConfig, oldEndpoints)
		if rollbackErr != nil {
			err = errors.Join(err, rollbackErr)
		}
		return m.failLocked(err)
	}
	if err := m.waitReadyLocked(ctx, compiled.Endpoints, compiled.ProxyCount); err != nil {
		_ = m.stopLocked(context.Background())
		rollbackErr := m.rollbackAndRestartLocked(ctx, hadOldConfig, oldEndpoints)
		if rollbackErr != nil {
			err = errors.Join(err, rollbackErr)
		}
		return m.failLocked(err)
	}

	now := time.Now().UTC()
	m.lastCompiled = compiled
	m.status.State = "running"
	m.status.Running = true
	m.status.PID = m.process.command.Process.Pid
	m.status.ListenerCount = len(compiled.Endpoints)
	m.status.ProxyCount = compiled.ProxyCount
	m.status.ActiveListeners = append([]Endpoint(nil), compiled.Endpoints...)
	m.status.LastApplyAt = &now
	m.status.LastError = ""
	return nil
}

func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.refreshProcessLocked()
	result := m.status
	result.ActiveListeners = append([]Endpoint(nil), m.status.ActiveListeners...)
	return result
}

func (m *Manager) Close(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.externalProcess {
		return nil
	}
	return m.stopLocked(ctx)
}

func (m *Manager) resolveBinary(value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		value = "mihomo"
	}
	resolved, err := exec.LookPath(value)
	if err != nil {
		m.binary = value
		return
	}
	m.binary = resolved
	m.available = true
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, resolved, "-v").CombinedOutput()
	if err == nil {
		m.version = strings.TrimSpace(string(output))
	}
}

func (m *Manager) validateLocked(ctx context.Context, path string) error {
	validationContext, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	command := exec.CommandContext(
		validationContext,
		m.binary,
		"-t",
		"-d", m.runtimeDir,
		"-f", path,
	)
	command.Env = m.commandEnvironment()
	_, err := command.CombinedOutput()
	if err != nil {
		m.logger.Error("Mihomo configuration validation failed", "error", err)
		return fmt.Errorf("mihomo config validation failed: %w", err)
	}
	return nil
}

func (m *Manager) startLocked(configPath string) error {
	if err := os.Remove(m.controllerSocket); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove stale Mihomo controller socket: %w", err)
	}
	logFile, err := newRotatingLogWriter(m.logPath, m.logMaxBytes, m.logBackups)
	if err != nil {
		return fmt.Errorf("open mihomo log: %w", err)
	}
	command := exec.Command(m.binary, "-d", m.runtimeDir, "-f", configPath)
	command.Env = m.commandEnvironment()
	command.Stdout = logFile
	command.Stderr = logFile
	if err := command.Start(); err != nil {
		_ = logFile.Close()
		return fmt.Errorf("start mihomo: %w", err)
	}
	item := &process{command: command, done: make(chan error, 1)}
	m.process = item
	go func() {
		err := command.Wait()
		_ = logFile.Close()
		item.done <- err
		close(item.done)
	}()
	return nil
}

func (m *Manager) reloadExternalLocked(ctx context.Context) error {
	payload := []byte(fmt.Sprintf(`{"path":%q}`, m.activePath))
	transport := &http.Transport{Proxy: nil, DialContext: func(dialContext context.Context, _, _ string) (net.Conn, error) {
		var dialer net.Dialer
		return dialer.DialContext(dialContext, "unix", m.controllerSocket)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 8 * time.Second}
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, "http://unix/configs?force=true", bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("create Mihomo reload request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("reload systemd-managed Mihomo: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("reload systemd-managed Mihomo returned status %d", response.StatusCode)
	}
	return nil
}

func (m *Manager) commandEnvironment() []string {
	prefix := "GOMAXPROCS="
	environment := make([]string, 0, len(os.Environ())+1)
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, prefix) {
			environment = append(environment, value)
		}
	}
	return append(environment, prefix+strconv.Itoa(m.maxProcs))
}

func (m *Manager) stopLocked(ctx context.Context) error {
	m.refreshProcessLocked()
	if m.process == nil {
		m.status.Running = false
		m.status.PID = 0
		return nil
	}
	item := m.process
	_ = item.command.Process.Signal(syscall.SIGTERM)
	select {
	case <-item.done:
	case <-ctx.Done():
		_ = item.command.Process.Kill()
		<-item.done
		m.process = nil
		m.status.Running = false
		m.status.PID = 0
		return ctx.Err()
	case <-time.After(5 * time.Second):
		_ = item.command.Process.Kill()
		<-item.done
	}
	m.process = nil
	m.status.Running = false
	m.status.PID = 0
	return nil
}

func (m *Manager) waitReadyLocked(ctx context.Context, endpoints []Endpoint, proxyCount int) error {
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		m.refreshProcessLocked()
		if !m.isRunningLocked() {
			return errors.New("mihomo exited before runtime became ready")
		}
		allReady := true
		for _, endpoint := range endpoints {
			address := probeAddress(endpoint.BindAddress, endpoint.Port)
			connection, err := net.DialTimeout("tcp", address, 250*time.Millisecond)
			if err != nil {
				allReady = false
				break
			}
			_ = connection.Close()
		}
		if allReady && proxyCount > 0 {
			connection, err := net.DialTimeout("unix", m.controllerSocket, 250*time.Millisecond)
			if err != nil {
				allReady = false
			} else {
				_ = connection.Close()
			}
		}
		if allReady {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return errors.New("mihomo listeners did not become ready")
}

func (m *Manager) refreshProcessLocked() {
	if m.externalProcess {
		m.status.Running = m.externalSocketReadyLocked()
		m.status.PID = 0
		if m.status.Running && m.status.State == "failed" {
			m.status.State = "running"
		}
		return
	}
	if m.process == nil {
		return
	}
	select {
	case err := <-m.process.done:
		m.process = nil
		m.status.Running = false
		m.status.PID = 0
		m.status.State = "failed"
		if err != nil {
			m.status.LastError = "mihomo exited: " + err.Error()
		} else {
			m.status.LastError = "mihomo exited"
		}
	default:
	}
}

func (m *Manager) isRunningLocked() bool {
	if m.externalProcess {
		return m.externalSocketReadyLocked()
	}
	return m.process != nil
}

func (m *Manager) externalSocketReadyLocked() bool {
	connection, err := net.DialTimeout("unix", m.controllerSocket, 100*time.Millisecond)
	if err != nil {
		return false
	}
	_ = connection.Close()
	return true
}

func (m *Manager) rollbackAndRestartLocked(ctx context.Context, hadOldConfig bool, oldEndpoints []Endpoint) error {
	if err := m.rollbackConfigLocked(hadOldConfig); err != nil {
		return err
	}
	if !hadOldConfig {
		return nil
	}
	if m.externalProcess {
		if err := m.reloadExternalLocked(ctx); err != nil {
			return fmt.Errorf("reload previous mihomo config: %w", err)
		}
		if err := m.waitReadyLocked(ctx, oldEndpoints, m.lastCompiled.ProxyCount); err != nil {
			return fmt.Errorf("previous mihomo config did not recover: %w", err)
		}
		m.status.Running = true
		m.status.PID = 0
		m.status.ListenerCount = len(oldEndpoints)
		m.status.ActiveListeners = append([]Endpoint(nil), oldEndpoints...)
		return nil
	}
	if err := m.startLocked(m.activePath); err != nil {
		return fmt.Errorf("restart previous mihomo config: %w", err)
	}
	if len(oldEndpoints) > 0 || m.lastCompiled.ProxyCount > 0 {
		if err := m.waitReadyLocked(ctx, oldEndpoints, m.lastCompiled.ProxyCount); err != nil {
			return fmt.Errorf("previous mihomo config did not recover: %w", err)
		}
	}
	m.lastCompiled.Endpoints = append([]Endpoint(nil), oldEndpoints...)
	m.status.Running = true
	m.status.PID = m.process.command.Process.Pid
	m.status.ListenerCount = len(oldEndpoints)
	m.status.ActiveListeners = append([]Endpoint(nil), oldEndpoints...)
	return nil
}

func (m *Manager) rollbackConfigLocked(hadOldConfig bool) error {
	if hadOldConfig {
		if err := copyFileAtomic(m.previous, m.activePath, 0o600); err != nil {
			return fmt.Errorf("restore previous mihomo config: %w", err)
		}
		return nil
	}
	if err := os.Remove(m.activePath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove failed mihomo config: %w", err)
	}
	return nil
}

func (m *Manager) failLocked(err error) error {
	m.recordFailureLocked(err)
	return err
}

func (m *Manager) recordFailureLocked(err error) {
	m.status.State = "failed"
	m.status.LastError = err.Error()
}

func writeFileAtomic(path string, content []byte, mode os.FileMode) error {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".candidate-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(mode); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(content); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	return syncDirectory(directory)
}

func copyFileAtomic(source, destination string, mode os.FileMode) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	content, err := io.ReadAll(io.LimitReader(input, 32<<20))
	if err != nil {
		return err
	}
	return writeFileAtomic(destination, content, mode)
}

func publishCandidate(candidate, active string) error {
	if err := os.Rename(candidate, active); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(active))
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func probeAddress(host string, port int) string {
	ip := net.ParseIP(host)
	if ip != nil && ip.IsUnspecified() {
		if ip.To4() != nil {
			host = "127.0.0.1"
		} else {
			host = "::1"
		}
	}
	return net.JoinHostPort(host, fmt.Sprintf("%d", port))
}
