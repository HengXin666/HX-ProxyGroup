package hxproxygroup

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestInstallScriptSyntax(t *testing.T) {
	t.Parallel()
	command := exec.Command("bash", "-n", "install.sh")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("bash -n install.sh failed: %v\n%s", err, output)
	}
}

func TestSystemdUnitKeepsManagementAPIPrivate(t *testing.T) {
	t.Parallel()
	content, err := os.ReadFile("deploy/systemd/hx-proxygroup.service")
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	unit := string(content)
	required := []string{
		"--listen 127.0.0.1:19090",
		"--master-key /var/lib/hx-proxygroup/master.key",
		"Restart=on-failure",
		"NoNewPrivileges=true",
		"ProtectSystem=strict",
		"ProtectHome=true",
		"ReadWritePaths=/var/lib/hx-proxygroup",
	}
	for _, expected := range required {
		if !strings.Contains(unit, expected) {
			t.Errorf("systemd unit is missing %q", expected)
		}
	}
}
