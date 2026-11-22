package xray

import (
	"strings"
	"testing"

	"hynode/internal/panel"
	"hynode/internal/state"
)

func TestSupportedProtocols(t *testing.T) {
	supported := []panel.NodeConfig{
		{Protocol: "vless", Network: "xhttp"},
		{Protocol: "vless", Network: "tcp", TLS: 2},
		{Protocol: "vmess", Network: "ws"},
		{Protocol: "trojan", Network: "grpc"},
		{Protocol: "shadowsocks"},
		{Protocol: "socks"},
		{Protocol: "http"},
	}
	for _, cfg := range supported {
		if !Supported(cfg) {
			t.Errorf("Supported(%+v) = false, want true", cfg)
		}
	}

	unsupported := []panel.NodeConfig{
		{Protocol: "hysteria"},
		{Protocol: "tuic"},
		{Protocol: "anytls"},
		{Protocol: "naive"},
		{Protocol: "mieru"},
		{Protocol: "vless", Network: "h2"},
		{Protocol: "shadowsocks", Plugin: "obfs", PluginOpts: "obfs=http"},
	}
	for _, cfg := range unsupported {
		if Supported(cfg) {
			t.Errorf("Supported(%+v) = true, want false", cfg)
		}
	}
}

func TestBuildVlessXHTTPReality(t *testing.T) {
	f := Factory{DataDir: t.TempDir()}
	raw, err := f.build("n1", panel.NodeConfig{
		Protocol:   "vless",
		ServerPort: 443,
		Network:    "xhttp",
		TLS:        2,
		Flow:       "xtls-rprx-vision",
		NetworkSettings: map[string]any{
			"path":  "/xh",
			"host":  "example.com",
			"mode":  "auto",
			"extra": map[string]any{"xPaddingBytes": "100-1000"},
		},
		TLSSettings: map[string]any{
			"server_name":    "www.example.com",
			"server_port":    "443",
			"private_key":    "server-private-key",
			"short_id":       "01234567",
			"allow_insecure": true,
		},
	}, []panel.User{{ID: 7, UUID: "u-uuid"}})
	if err != nil {
		t.Fatal(err)
	}

	inbounds := raw["inbounds"].([]any)
	in := inbounds[0].(map[string]any)
	if in["protocol"] != "vless" {
		t.Fatalf("protocol = %v", in["protocol"])
	}
	settings := in["settings"].(map[string]any)
	if settings["decryption"] != "none" {
		t.Fatalf("decryption = %v", settings["decryption"])
	}
	clients := settings["clients"].([]map[string]any)
	client := clients[0]
	if client["id"] != "u-uuid" || client["email"] != "7" {
		t.Fatalf("client = %#v", client)
	}
	if client["flow"] != "xtls-rprx-vision" {
		t.Fatalf("flow = %v", client["flow"])
	}

	stream := in["streamSettings"].(map[string]any)
	if stream["network"] != "xhttp" {
		t.Fatalf("network = %v", stream["network"])
	}
	if stream["security"] != "reality" {
		t.Fatalf("security = %v", stream["security"])
	}

	xhttp := stream["xhttpSettings"].(map[string]any)
	if xhttp["path"] != "/xh" || xhttp["mode"] != "auto" {
		t.Fatalf("xhttpSettings = %#v", xhttp)
	}
	// Advanced options nested under panel "extra" must be flattened.
	if xhttp["xPaddingBytes"] != "100-1000" {
		t.Fatalf("xPaddingBytes not flattened: %#v", xhttp)
	}

	reality := stream["realitySettings"].(map[string]any)
	if reality["dest"] != "www.example.com:443" {
		t.Fatalf("dest = %v", reality["dest"])
	}
	if reality["privateKey"] != "server-private-key" {
		t.Fatalf("privateKey = %v", reality["privateKey"])
	}
	names := reality["serverNames"].([]string)
	if len(names) != 1 || names[0] != "www.example.com" {
		t.Fatalf("serverNames = %#v", names)
	}
	ids := reality["shortIds"].([]string)
	if len(ids) != 1 || ids[0] != "01234567" {
		t.Fatalf("shortIds = %#v", ids)
	}
}

func TestBuildRejectsNonNoneVlessDecryption(t *testing.T) {
	f := Factory{DataDir: t.TempDir()}
	_, err := f.build("n", panel.NodeConfig{
		Protocol:   "vless",
		ServerPort: 443,
		Decryption: "mlkem768x25519plus-native-10s-1024c",
	}, nil)
	if err == nil {
		t.Fatal("expected decryption error")
	}
	if !strings.Contains(err.Error(), "decryption") {
		t.Fatalf("err = %v", err)
	}
}

func TestCompilePanelRoutes(t *testing.T) {
	rules, err := compilePanelRoutes([]panel.Route{
		{ID: 1, Match: []string{"*.example.com", "10.0.0.0/8"}, Action: "block"},
		{ID: 2, Match: []string{"service.example"}, Action: "direct"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 3 {
		t.Fatalf("rules = %#v", rules)
	}
	first := rules[0].(map[string]any)
	if first["outboundTag"] != "block" {
		t.Fatalf("outboundTag = %v", first["outboundTag"])
	}
	domains := first["domain"].([]string)
	if domains[0] != "domain:example.com" {
		t.Fatalf("domains = %#v", domains)
	}
}

func TestFactoryCreatesAndClosesInstance(t *testing.T) {
	runtime, err := (Factory{DataDir: t.TempDir()}).New("n", panel.NodeConfig{
		Protocol:   "vless",
		ListenIP:   "127.0.0.1",
		ServerPort: 0, // invalid — expect validation error below
	}, []panel.User{{ID: 1, UUID: "password"}}, state.New())
	if err == nil {
		_ = runtime.Close()
		// port 0 should fail validation
		t.Fatal("expected invalid server_port error")
	}

	runtime, err = (Factory{DataDir: t.TempDir()}).New("n", panel.NodeConfig{
		Protocol:   "vless",
		ListenIP:   "127.0.0.1",
		ServerPort: 18443,
		Network:    "tcp",
	}, []panel.User{{ID: 1, UUID: "3c8a1f0e-0000-4000-8000-000000000001"}}, state.New())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = runtime.Close() }()
}

func TestReloadUsersKeepsProtocol(t *testing.T) {
	rt, err := (Factory{DataDir: t.TempDir()}).New("n", panel.NodeConfig{
		Protocol:   "socks",
		ListenIP:   "127.0.0.1",
		ServerPort: 18445,
	}, []panel.User{{ID: 1, UUID: "pw1"}}, state.New())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rt.Close() }()
	if err := rt.ReloadUsers([]panel.User{{ID: 1, UUID: "pw2"}, {ID: 2, UUID: "pw3"}}); err != nil {
		t.Fatal(err)
	}
}
