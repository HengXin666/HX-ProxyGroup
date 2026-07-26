package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConfigValidateAcceptsExplicitLoopbackAddresses(t *testing.T) {
	t.Parallel()

	for _, address := range []string{"127.0.0.1:19090", "[::1]:19090"} {
		config := validTestConfig(t)
		config.ListenAddress = address
		if err := config.Validate(); err != nil {
			t.Errorf("Validate(%q) error = %v", address, err)
		}
	}
}

func TestConfigValidateRejectsPublicAndAmbiguousAddresses(t *testing.T) {
	t.Parallel()

	addresses := []string{
		"0.0.0.0:19090",
		"[::]:19090",
		"192.0.2.10:19090",
		"localhost:19090",
		"127.0.0.1:0",
		"127.0.0.1:65536",
		"127.0.0.1:http",
		"127.0.0.1",
	}
	for _, address := range addresses {
		config := validTestConfig(t)
		config.ListenAddress = address
		if err := config.Validate(); err == nil {
			t.Errorf("Validate(%q) error = nil, want rejection", address)
		}
	}
}

func TestConfigEnsureDirectories(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	config := Config{
		ListenAddress:     "127.0.0.1:19090",
		DataDirectory:     filepath.Join(root, "data"),
		ApplicationConfig: filepath.Join(root, "etc", "config.yaml"),
		DatabasePath:      filepath.Join(root, "data", "db", "hx-proxygroup.db"),
		MasterKeyPath:     filepath.Join(root, "data", "master.key"),
		RuntimeConfigPath: filepath.Join(root, "data", "runtime", "active.yaml"),
		SnapshotsPath:     filepath.Join(root, "data", "snapshots"),
	}
	if err := config.EnsureDirectories(); err != nil {
		t.Fatalf("EnsureDirectories() error = %v", err)
	}

	directories := []string{
		config.DataDirectory,
		filepath.Join(config.DataDirectory, "artifacts"),
		filepath.Dir(config.DatabasePath),
		filepath.Dir(config.RuntimeConfigPath),
		config.SnapshotsPath,
	}
	for _, directory := range directories {
		info, err := os.Stat(directory)
		if err != nil {
			t.Errorf("Stat(%q) error = %v", directory, err)
			continue
		}
		if !info.IsDir() {
			t.Errorf("%q is not a directory", directory)
		}
	}
}

func validTestConfig(t *testing.T) Config {
	t.Helper()
	root := t.TempDir()
	return Config{
		ListenAddress:     "127.0.0.1:19090",
		DataDirectory:     filepath.Join(root, "data"),
		ApplicationConfig: filepath.Join(root, "config.yaml"),
		DatabasePath:      filepath.Join(root, "data", "hx-proxygroup.db"),
		MasterKeyPath:     filepath.Join(root, "data", "master.key"),
		RuntimeConfigPath: filepath.Join(root, "data", "runtime", "active.yaml"),
		SnapshotsPath:     filepath.Join(root, "data", "snapshots"),
	}
}
