package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/HengXin666/HX-ProxyGroup/internal/alert"
	"github.com/HengXin666/HX-ProxyGroup/internal/api"
	"github.com/HengXin666/HX-ProxyGroup/internal/artifact"
	"github.com/HengXin666/HX-ProxyGroup/internal/auth"
	"github.com/HengXin666/HX-ProxyGroup/internal/bundle"
	"github.com/HengXin666/HX-ProxyGroup/internal/config"
	"github.com/HengXin666/HX-ProxyGroup/internal/dataplane/mihomo"
	"github.com/HengXin666/HX-ProxyGroup/internal/instance"
	"github.com/HengXin666/HX-ProxyGroup/internal/listener"
	"github.com/HengXin666/HX-ProxyGroup/internal/metrics"
	"github.com/HengXin666/HX-ProxyGroup/internal/node"
	"github.com/HengXin666/HX-ProxyGroup/internal/nodeparse"
	"github.com/HengXin666/HX-ProxyGroup/internal/proxygroup"
	"github.com/HengXin666/HX-ProxyGroup/internal/proxylog"
	"github.com/HengXin666/HX-ProxyGroup/internal/proxyservice"
	"github.com/HengXin666/HX-ProxyGroup/internal/residential"
	"github.com/HengXin666/HX-ProxyGroup/internal/routingrules"
	"github.com/HengXin666/HX-ProxyGroup/internal/scheduler"
	"github.com/HengXin666/HX-ProxyGroup/internal/secret"
	"github.com/HengXin666/HX-ProxyGroup/internal/store"
	"github.com/HengXin666/HX-ProxyGroup/internal/subscription"
	"github.com/HengXin666/HX-ProxyGroup/internal/systemsettings"
	"github.com/HengXin666/HX-ProxyGroup/internal/terminal"
	"github.com/HengXin666/HX-ProxyGroup/internal/transfer"
)

var version = "dev"

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if len(os.Args) > 1 && os.Args[1] == "--terminal-helper" {
		if err := runTerminalHelper(logger, os.Args[2:]); err != nil {
			logger.Error("terminal helper stopped", "error", err)
			os.Exit(1)
		}
		return
	}
	if err := run(logger); err != nil {
		logger.Error("service stopped", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg := config.Default()
	flag.StringVar(&cfg.ListenAddress, "listen", cfg.ListenAddress, "HTTP management API listen address")
	flag.StringVar(&cfg.DataDirectory, "data-dir", cfg.DataDirectory, "persistent data directory")
	flag.StringVar(&cfg.ApplicationConfig, "config", cfg.ApplicationConfig, "application configuration path")
	flag.StringVar(&cfg.DatabasePath, "database", cfg.DatabasePath, "SQLite database path")
	flag.StringVar(&cfg.MasterKeyPath, "master-key", cfg.MasterKeyPath, "master key path for encrypted secrets")
	flag.StringVar(&cfg.RuntimeConfigPath, "runtime-config", cfg.RuntimeConfigPath, "active Mihomo configuration path")
	flag.StringVar(&cfg.SnapshotsPath, "snapshots", cfg.SnapshotsPath, "subscription snapshot directory")
	flag.StringVar(&cfg.WebRoot, "web-root", cfg.WebRoot, "production web asset directory")
	flag.StringVar(&cfg.MihomoBinary, "mihomo", cfg.MihomoBinary, "Mihomo executable path or command name")
	flag.BoolVar(&cfg.MihomoExternal, "mihomo-external", cfg.MihomoExternal, "coordinate a systemd-managed Mihomo process")
	flag.StringVar(&cfg.MihomoEgressInterface, "mihomo-egress-interface", cfg.MihomoEgressInterface, "Mihomo outbound interface: auto, off, or an interface name")
	flag.IntVar(&cfg.MihomoMaxProcs, "mihomo-max-procs", cfg.MihomoMaxProcs, "maximum CPU threads available to Mihomo")
	flag.Int64Var(&cfg.MihomoLogMaxBytes, "mihomo-log-max-bytes", cfg.MihomoLogMaxBytes, "maximum bytes in each Mihomo log file")
	flag.IntVar(&cfg.MihomoLogBackups, "mihomo-log-backups", cfg.MihomoLogBackups, "number of rotated Mihomo log files to keep")
	flag.StringVar(&cfg.TerminalPrivilegedSocket, "terminal-socket", cfg.TerminalPrivilegedSocket, "local root PTY helper socket")
	flag.Parse()

	if err := cfg.Validate(); err != nil {
		return err
	}
	if err := cfg.EnsureDirectories(); err != nil {
		return err
	}
	instanceLock, err := instance.Acquire(cfg.DataDirectory)
	if err != nil {
		return err
	}
	defer instanceLock.Close()
	managementListener, err := net.Listen("tcp", cfg.ListenAddress)
	if err != nil {
		return fmt.Errorf("listen on management API address %s: %w", cfg.ListenAddress, err)
	}
	defer managementListener.Close()

	startupContext, cancelStartup := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelStartup()
	database, err := store.Open(startupContext, cfg.DatabasePath)
	if err != nil {
		return err
	}
	defer database.Close()
	if err := database.SetMetadata(startupContext, "application_version", version); err != nil {
		return err
	}
	databaseStatus, err := database.Status(startupContext)
	if err != nil {
		return err
	}
	secretBox, err := secret.LoadOrCreate(cfg.MasterKeyPath)
	if err != nil {
		return err
	}
	mihomoCompiler, err := mihomo.NewCompiler(database, secretBox)
	if err != nil {
		return err
	}
	egressInterface, egressErr := mihomo.ResolveEgressInterface(cfg.MihomoEgressInterface)
	if egressErr != nil {
		if !strings.EqualFold(strings.TrimSpace(cfg.MihomoEgressInterface), "auto") {
			return egressErr
		}
		logger.Warn("automatic Mihomo egress isolation unavailable", "error", egressErr)
	} else if egressInterface != "" {
		logger.Info("Mihomo outbound traffic isolated from host TUN routing", "interface", egressInterface)
	}
	mihomoManager, err := mihomo.NewManager(
		mihomoCompiler,
		cfg.MihomoBinary,
		cfg.RuntimeConfigPath,
		logger,
		mihomo.WithEgressInterface(egressInterface),
		mihomo.WithProcessMaxProcs(cfg.MihomoMaxProcs),
		mihomo.WithLogRotation(cfg.MihomoLogMaxBytes, cfg.MihomoLogBackups),
		mihomo.WithExternalProcess(cfg.MihomoExternal),
	)
	if err != nil {
		return err
	}
	defer func() {
		shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := mihomoManager.Close(shutdownContext); err != nil {
			logger.Error("shutdown Mihomo", "error", err)
		}
	}()
	subscriptionService, err := subscription.NewService(
		database,
		secretBox,
		subscription.WithRefresh(subscription.NewDefaultSourceLoader(), cfg.SnapshotsPath),
		subscription.WithParser(nodeparse.Parse),
		subscription.WithReconciler(mihomoManager),
	)
	if err != nil {
		return err
	}
	nodeService, err := node.NewService(database, node.WithProber(mihomoManager))
	if err != nil {
		return err
	}
	settingsService, err := systemsettings.NewService(database, mihomoManager)
	if err != nil {
		return err
	}
	routingRulesService, err := routingrules.NewService(database, mihomoManager)
	if err != nil {
		return err
	}
	proxyGroupService, err := proxygroup.NewService(database, mihomoManager)
	if err != nil {
		return err
	}
	listenerService, err := listener.NewService(database, secretBox, mihomoManager)
	if err != nil {
		return err
	}
	proxyService, err := proxyservice.NewService(proxyGroupService, listenerService)
	if err != nil {
		return err
	}
	residentialService, err := residential.NewService(
		database,
		secretBox,
		proxyGroupService,
		listenerService,
		residential.WithSelector(mihomoManager),
		residential.WithReachabilityChecker(mihomoManager),
		residential.WithSessionRouter(mihomoManager),
	)
	if err != nil {
		return err
	}
	trafficService, err := metrics.NewService(database, mihomoManager, logger, metrics.Config{})
	if err != nil {
		return err
	}
	proxyLogService, err := proxylog.NewDefaultService(mihomoManager)
	if err != nil {
		return err
	}
	logHandler, err := api.NewLogHandler(proxyLogService, listenerService, proxyGroupService)
	if err != nil {
		return err
	}
	authService, err := auth.NewService(
		database,
		filepath.Join(cfg.DataDirectory, "admin-setup-token"),
		logger,
		secretBox,
	)
	if err != nil {
		return err
	}
	if err := authService.EnsureSetupToken(startupContext); err != nil {
		return err
	}
	if err := mihomoManager.Apply(startupContext); err != nil {
		logger.Error("initial Mihomo apply failed; management API remains available", "error", err)
	}
	subscriptionScheduler, err := scheduler.NewSubscriptionScheduler(
		database,
		subscriptionService,
		logger,
		scheduler.SubscriptionConfig{},
	)
	if err != nil {
		return err
	}
	nodeScheduler, err := scheduler.NewNodeScheduler(nodeService, logger, scheduler.NodeConfig{})
	if err != nil {
		return err
	}
	residentialScheduler, err := scheduler.NewResidentialScheduler(
		residentialService,
		logger,
		scheduler.ResidentialConfig{},
	)
	if err != nil {
		return err
	}
	alertService, err := alert.NewService(
		database,
		secretBox,
		[]alert.Detector{
			alert.NewSubscriptionDetector(database),
			alert.NewEmptyGroupDetector(database),
			alert.NewDataPlaneDetector(func() alert.DataPlaneStatus {
				status := mihomoManager.Status()
				return alert.DataPlaneStatus{
					Available: status.Available,
					Running:   status.Running,
					LastError: status.LastError,
				}
			}),
		},
		logger,
	)
	if err != nil {
		return err
	}
	alertScheduler, err := scheduler.NewAlertScheduler(alertService, logger, scheduler.AlertConfig{})
	if err != nil {
		return err
	}
	terminalService, err := terminal.NewService(terminal.Config{
		Enabled:          cfg.TerminalEnabled,
		Shell:            cfg.TerminalShell,
		PrivilegedSocket: cfg.TerminalPrivilegedSocket,
	}, logger)
	if err != nil {
		return err
	}
	defer terminalService.Shutdown()

	portableStatePath := filepath.Join(cfg.DataDirectory, "state", "control-plane.json")
	if err := writePortableState(portableStatePath, cfg, databaseStatus.SchemaVersion); err != nil {
		return err
	}

	catalog, err := artifact.NewCatalog(filepath.Join(cfg.DataDirectory, "artifacts"))
	if err != nil {
		return err
	}
	baseSources := []bundle.Source{
		{
			Name:     "control-plane-state",
			Path:     portableStatePath,
			Scope:    bundle.ScopeBackup | bundle.ScopeExport,
			Required: true,
		},
		{
			Name:      "application-config",
			Path:      cfg.ApplicationConfig,
			Scope:     bundle.ScopeBackup,
			Required:  false,
			Sensitive: true,
		},
		{
			Name:      "runtime-config",
			Path:      cfg.RuntimeConfigPath,
			Scope:     bundle.ScopeBackup,
			Required:  false,
			Sensitive: true,
		},
		{
			Name:      "subscription-snapshots",
			Path:      cfg.SnapshotsPath,
			Scope:     bundle.ScopeBackup,
			Required:  false,
			Sensitive: true,
		},
	}
	transfers, err := transfer.NewService(
		catalog,
		baseSources,
		database,
		filepath.Join(cfg.DataDirectory, "tmp"),
		version,
	)
	if err != nil {
		return err
	}
	apiServer, err := api.NewServer(
		transfers,
		logger,
		api.WithSubscriptions(subscriptionService),
		api.WithNodes(nodeService),
		api.WithProxyGroups(proxyGroupService),
		api.WithListeners(listenerService),
		api.WithProxyServices(proxyService),
		api.WithResidential(residentialService),
		api.WithTraffic(trafficService),
		api.WithSettings(settingsService),
		api.WithRoutingRules(routingRulesService),
		api.WithLogs(logHandler),
		api.WithDataPlane(mihomoManager),
		api.WithOverview(mihomoManager),
		api.WithAuth(authService),
		api.WithAlerts(alertService),
		api.WithTerminal(terminalService),
		api.WithWebRoot(cfg.WebRoot),
		api.WithSystemInfo(api.SystemInfo{
			Application: "HX-ProxyGroup", Version: version,
			RepositoryURL:      "https://github.com/HengXin666/HX-ProxyGroup",
			UpdateCommand:      "sudo hx-proxygroup-install upgrade",
			SupportedProtocols: nodeparse.SupportedProtocols(),
		}),
	)
	if err != nil {
		return err
	}

	httpServer := &http.Server{
		Addr:              cfg.ListenAddress,
		Handler:           apiServer.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      5 * time.Minute,
		IdleTimeout:       90 * time.Second,
		MaxHeaderBytes:    32 << 10,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	serverErrors := make(chan error, 1)
	go func() {
		logger.Info(
			"management API listening",
			"address", cfg.ListenAddress,
			"version", version,
			"database_schema", databaseStatus.SchemaVersion,
			"database_journal", databaseStatus.JournalMode,
		)
		if err := httpServer.Serve(managementListener); !errors.Is(err, http.ErrServerClosed) {
			serverErrors <- err
			return
		}
		serverErrors <- nil
	}()

	const backgroundTasks = 5
	backgroundErrors := make(chan error, backgroundTasks)
	go func() {
		backgroundErrors <- subscriptionScheduler.Run(ctx)
	}()
	go func() {
		backgroundErrors <- nodeScheduler.Run(ctx)
	}()
	go func() {
		backgroundErrors <- residentialScheduler.Run(ctx)
	}()
	go func() {
		backgroundErrors <- alertScheduler.Run(ctx)
	}()
	go func() {
		backgroundErrors <- trafficService.Run(ctx)
	}()
	drainBackground := func(alreadyRead int) error {
		var joined error
		for index := alreadyRead; index < backgroundTasks; index++ {
			joined = errors.Join(joined, <-backgroundErrors)
		}
		return joined
	}

	shutdownHTTP := func() error {
		apiServer.SetReady(false)
		shutdownContext, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdownContext); err != nil {
			return fmt.Errorf("shutdown HTTP server: %w", err)
		}
		return nil
	}

	select {
	case serverErr := <-serverErrors:
		stop()
		return errors.Join(serverErr, drainBackground(0))
	case firstBackgroundErr := <-backgroundErrors:
		stop()
		shutdownErr := shutdownHTTP()
		serverErr := <-serverErrors
		return errors.Join(firstBackgroundErr, drainBackground(1), shutdownErr, serverErr)
	case <-ctx.Done():
		shutdownErr := shutdownHTTP()
		serverErr := <-serverErrors
		return errors.Join(shutdownErr, serverErr, drainBackground(0))
	}
}

func runTerminalHelper(logger *slog.Logger, arguments []string) error {
	flags := flag.NewFlagSet("terminal-helper", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	var config terminal.HelperConfig
	flags.StringVar(&config.SocketPath, "terminal-socket", "/run/hx-proxygroup/terminal.sock", "Unix socket path")
	flags.StringVar(&config.SocketGroup, "terminal-socket-group", "hx-proxygroup", "Unix socket group")
	flags.StringVar(&config.AllowedUser, "terminal-helper-user", "hx-proxygroup", "only accept this local Unix socket user")
	flags.StringVar(&config.Shell, "terminal-shell", "", "shell executable")
	flags.IntVar(&config.MaxSessions, "terminal-max-sessions", 2, "maximum helper sessions")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	return terminal.RunHelper(ctx, config, logger)
}

type portableState struct {
	SchemaVersion         int      `json:"schema_version"`
	DatabaseSchemaVersion int      `json:"database_schema_version"`
	Application           string   `json:"application"`
	Version               string   `json:"version"`
	ListenAddress         string   `json:"listen_address"`
	Features              []string `json:"features"`
}

func writePortableState(destination string, cfg config.Config, databaseSchemaVersion int) error {
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}
	state := portableState{
		SchemaVersion:         1,
		DatabaseSchemaVersion: databaseSchemaVersion,
		Application:           "HX-ProxyGroup",
		Version:               version,
		ListenAddress:         cfg.ListenAddress,
		Features: []string{
			"backup",
			"portable-export",
			"artifact-verification",
			"sqlite-wal",
			"sqlite-online-backup",
			"traffic-statistics",
		},
	}
	encoded, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode portable state: %w", err)
	}
	encoded = append(encoded, '\n')
	temporary, err := os.CreateTemp(filepath.Dir(destination), ".state-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary state file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return fmt.Errorf("secure state file: %w", err)
	}
	if _, err := temporary.Write(encoded); err != nil {
		return fmt.Errorf("write state file: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync state file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close state file: %w", err)
	}
	if err := os.Rename(temporaryPath, destination); err != nil {
		return fmt.Errorf("publish state file: %w", err)
	}
	return nil
}
