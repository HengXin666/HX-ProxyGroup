package mihomo

import (
	"strings"
	"testing"
)

func TestParseDefaultRouteInterfaceUsesLowestActiveMetric(t *testing.T) {
	t.Parallel()
	routes := `Iface Destination Gateway Flags RefCnt Use Metric Mask MTU Window IRTT
tun0 00000000 00000000 0000 0 0 1 00000000 0 0 0
eth1 00000000 0100000A 0003 0 0 200 00000000 0 0 0
eth0 00000000 0100000A 0003 0 0 100 00000000 0 0 0
eth2 0000000A 00000000 0001 0 0 1 00FFFFFF 0 0 0
`
	name, err := parseDefaultRouteInterface(strings.NewReader(routes))
	if err != nil {
		t.Fatalf("parseDefaultRouteInterface() error = %v", err)
	}
	if name != "eth0" {
		t.Fatalf("interface = %q, want eth0", name)
	}
}

func TestParseDefaultRouteInterfaceRejectsMissingDefault(t *testing.T) {
	t.Parallel()
	_, err := parseDefaultRouteInterface(strings.NewReader("Iface Destination Gateway Flags RefCnt Use Metric Mask\neth0 0000000A 0 1 0 0 0 00FFFFFF\n"))
	if err == nil {
		t.Fatal("parseDefaultRouteInterface() error = nil, want rejection")
	}
}

func TestResolveEgressInterfaceAllowsExplicitDisable(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"off", "none", "disabled"} {
		name, err := ResolveEgressInterface(value)
		if err != nil || name != "" {
			t.Errorf("ResolveEgressInterface(%q) = %q, %v; want empty, nil", value, name, err)
		}
	}
}
