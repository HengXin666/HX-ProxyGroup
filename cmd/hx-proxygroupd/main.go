package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/HengXin666/HX-ProxyGroup/internal/alert"
	"github.com/HengXin666/HX-ProxyGroup/internal/api"
	"github.com/HengXin666/HX-ProxyGroup/internal/artifact"
	"github.com/HengXin666/HX-ProxyGroup/internal/auth"
	"github.com/HengXin666/HX-ProxyGroup/internal/bundle"
	"github.com/HengXin666/HX-ProxyGroup/internal/config"
	"github.com/HengXin666/HX-ProxyGroup/internal/dataplane/mihomo"
	"github.com/HengXin666/HX-ProxyGroup/internal/listener"
	"github.com/HengXin666/HX-ProxyGroup/internal/node"
	"github.com/HengXin666/HX-ProxyGroup/internal/nodeparse"
	"github.com/HengXin666/HX-ProxyGroup/internal/proxygroup"
	"github.com/HengXin666/HX-ProxyGroup/internal/proxyservice"
	"github.com/HengXin666/HX-ProxyGroup/internal/scheduler"
	"github.com/HengXin666/HX-ProxyGroup/internal/secret"
	"github.com/HengXin666/HX-ProxyGroup/internal/store"
	"github.com/HengXin666/HX-ProxyGroup/internal/subscription"
	"github.com/HengXin666/HX-ProxyGroup/internal/transfer"
)

var version = "dev"

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
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
	flag.StringVar(&cfg.MihomoBinary, "mihomo", cfg.MihomoBinary, "Mihomo executable path or command name")
	flag.Parse()

	if err := cfg.Validate(); err != nil {
		return err
	}
	if err := cfg.EnsureDirectories(); err != nil {
		return err
	}

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
	mihomoManager, err := mihomo.NewManager(
		mihomoCompiler,
		cfg.MihomoBinary,
		cfg.RuntimeConfigPath,
		logger,
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
	authService, err := auth.NewService(
		database,
		filepath.Join(cfg.DataDirectory, "admin-setup-token"),
		logger,
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
		api.WithDataPlane(mihomoManager),
		api.WithAuth(authService),
		api.WithAlerts(alertService),
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
		if err := httpServer.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			serverErrors <- err
			return
		}
		serverErrors <- nil
	}()

	const backgroundTasks = 3
	backgroundErrors := make(chan error, backgroundTasks)
	go func() {
		backgroundErrors <- subscriptionScheduler.Run(ctx)
	}()
	go func() {
		backgroundErrors <- nodeScheduler.Run(ctx)
	}()
	go func() {
		backgroundErrors <- alertScheduler.Run(ctx)
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
