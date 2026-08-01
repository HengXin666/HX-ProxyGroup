package mihomo

import (
	"testing"

	"github.com/HengXin666/HX-ProxyGroup/internal/store"
)

func TestCompileResidentialClientRulesKeepSessionsIndependent(t *testing.T) {
	t.Parallel()
	nodes := map[string]compiledNode{
		"node-a": {Name: "hx-node-0123456789abcdef"},
		"node-b": {Name: "hx-node-fedcba9876543210"},
	}
	rules, err := compileResidentialClientRules([]store.ResidentialClientRouteRecord{
		{ResidentialClientSessionRecord: store.ResidentialClientSessionRecord{
			SessionID: "window-01", AuthUsername: "hx-session-one", RouteMode: "residential",
			NodeFingerprint: "0123456789abcdef9999",
		}, ListenerID: "listener-channel-a", ChannelEnabled: true},
		{ResidentialClientSessionRecord: store.ResidentialClientSessionRecord{
			SessionID: "window-02", AuthUsername: "hx-session-two", RouteMode: "direct",
		}, ListenerID: "listener-channel-a", ChannelEnabled: true},
		{ResidentialClientSessionRecord: store.ResidentialClientSessionRecord{
			SessionID: "window-03", AuthUsername: "hx-session-three", RouteMode: "upstream",
		}, ListenerID: "listener-channel-a", UpstreamGroup: "low-cost-egress", ChannelEnabled: true},
	}, nodes)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"AND,((IN-NAME,hx-in-channel-a),(IN-USER,hx-session-one)),hx-node-0123456789abcdef",
		"AND,((IN-NAME,hx-in-channel-a),(IN-USER,hx-session-two)),DIRECT",
		"AND,((IN-NAME,hx-in-channel-a),(IN-USER,hx-session-three)),low-cost-egress",
	}
	if len(rules) != len(want) {
		t.Fatalf("rules = %v", rules)
	}
	for index := range want {
		if rules[index] != want[index] {
			t.Fatalf("rule %d = %q, want %q", index, rules[index], want[index])
		}
	}
}

func TestCompileListenerAddsResidentialClientCredentials(t *testing.T) {
	t.Parallel()
	compiler := &Compiler{cipher: benchmarkCipher{}}
	config, err := compiler.compileListener(store.ListenerRecord{
		ID: "listener-1", Name: "residential", Kind: "mixed",
		BindAddress: "127.0.0.1", Port: 18088, AuthMode: "none",
	}, "residential-group", []store.ResidentialClientRouteRecord{{
		ResidentialClientSessionRecord: store.ResidentialClientSessionRecord{
			ChannelID: "channel-1", SessionID: "window-01",
			AuthUsername: "hx-session-one", AuthPasswordEncrypted: []byte("session-secret"),
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	users, ok := config["users"].([]map[string]any)
	if !ok || len(users) != 1 {
		t.Fatalf("listener users = %#v", config["users"])
	}
	if users[0]["username"] != "hx-session-one" || users[0]["password"] != "session-secret" {
		t.Fatalf("listener user = %#v", users[0])
	}
	if forcedProxy, exists := config["proxy"]; exists {
		t.Fatalf("session-aware listener forces proxy %v and bypasses IN-USER rules", forcedProxy)
	}
}

func TestCompileListenerWithoutClientRoutesKeepsForcedProxy(t *testing.T) {
	t.Parallel()
	compiler := &Compiler{cipher: benchmarkCipher{}}
	config, err := compiler.compileListener(store.ListenerRecord{
		ID: "listener-1", Name: "residential", Kind: "mixed",
		BindAddress: "127.0.0.1", Port: 18088, AuthMode: "none",
	}, "residential-group", nil)
	if err != nil {
		t.Fatal(err)
	}
	if config["proxy"] != "residential-group" {
		t.Fatalf("listener proxy = %v, want residential-group", config["proxy"])
	}
}
