package singbox

import (
	"strings"
	"testing"

	"hynode/internal/panel"
	"hynode/internal/state"
)

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
}

func TestCompilePanelRouteRejectsUnboundProxy(t *testing.T) {
	if _, err := compilePanelRoutes([]panel.Route{{ID: 3, Match: []string{"example.com"}, Action: "proxy"}}); err == nil {
		t.Fatal("expected proxy route without outbound tag to fail")
	}
}

func TestFactoryAcceptsStandardBlockRoute(t *testing.T) {
	runtime, err := (Factory{DataDir: t.TempDir()}).New("n", panel.NodeConfig{
		Protocol:   "socks",
		ListenIP:   "127.0.0.1",
		ServerPort: 10800,
		Routes:     []panel.Route{{ID: 1, Match: []string{"example.com"}, Action: "block"}},
	}, []panel.User{{ID: 1, UUID: "password"}}, state.New())
	if err != nil {
		t.Fatal(err)
	}
	_ = runtime.Close()
}

func TestRealityInboundTLSUsesSingBoxServerSchema(t *testing.T) {
	for _, protocol := range []string{"vless", "trojan"} {
		t.Run(protocol, func(t *testing.T) {
			inbound, err := (Factory{}).inbound("n", panel.NodeConfig{
				Protocol:   protocol,
				ServerPort: 443,
				TLS:        2,
				TLSSettings: map[string]any{
					"server_name":    "www.example.com",
					"server_port":    "443",
					"public_key":     "client-side-public-key",
					"private_key":    "server-side-private-key",
					"short_id":       "01234567",
					"allow_insecure": true,
				},
			}, []panel.User{{ID: 1, UUID: "password"}})
			if err != nil {
				t.Fatal(err)
			}

			tlsConfig := inbound["tls"].(map[string]any)
			if tlsConfig["enabled"] != true {
				t.Fatalf("tls.enabled = %#v", tlsConfig["enabled"])
			}
			if tlsConfig["server_name"] != "www.example.com" {
				t.Fatalf("tls.server_name = %#v", tlsConfig["server_name"])
			}

			reality := tlsConfig["reality"].(map[string]any)
			if reality["enabled"] != true {
				t.Fatalf("reality.enabled = %#v", reality["enabled"])
			}
			if reality["private_key"] != "server-side-private-key" {
				t.Fatalf("reality.private_key = %#v", reality["private_key"])
			}
			if reality["short_id"] != "01234567" {
				t.Fatalf("reality.short_id = %#v", reality["short_id"])
			}

			handshake := reality["handshake"].(map[string]any)
			if handshake["server"] != "www.example.com" {
				t.Fatalf("handshake.server = %#v", handshake["server"])
			}
			if handshake["server_port"] != 443 {
				t.Fatalf("handshake.server_port = %#v", handshake["server_port"])
			}

			for _, invalidKey := range []string{"public_key", "allow_insecure", "server_name", "server_port"} {
				if _, ok := reality[invalidKey]; ok {
					t.Fatalf("reality contains invalid key %q: %#v", invalidKey, reality)
				}
			}
		})
	}
}

func TestRealityInboundTLSPortVariants(t *testing.T) {
	for name, value := range map[string]any{
		"string":  "443",
		"int":     443,
		"float64": float64(443),
	} {
		t.Run(name, func(t *testing.T) {
			tlsConfig, err := buildRealityInboundTLS(map[string]any{
				"server_name": "www.example.com",
				"server_port": value,
				"private_key": "server-side-private-key",
			})
			if err != nil {
				t.Fatal(err)
			}
			reality := tlsConfig["reality"].(map[string]any)
			handshake := reality["handshake"].(map[string]any)
			if handshake["server_port"] != 443 {
				t.Fatalf("handshake.server_port = %#v", handshake["server_port"])
			}
		})
	}
}

func TestRealityInboundTLSRejectsInvalidSettings(t *testing.T) {
	valid := func() map[string]any {
		return map[string]any{
			"server_name": "www.example.com",
			"server_port": "443",
			"private_key": "server-side-private-key",
		}
	}

	cases := map[string]struct {
		mutate func(map[string]any)
		want   string
	}{
		"missing server_name": {func(settings map[string]any) { delete(settings, "server_name") }, "server_name"},
		"empty server_name":   {func(settings map[string]any) { settings["server_name"] = " " }, "server_name"},
		"missing private_key": {func(settings map[string]any) { delete(settings, "private_key") }, "private_key"},
		"empty private_key":   {func(settings map[string]any) { settings["private_key"] = " " }, "private_key"},
		"missing server_port": {func(settings map[string]any) { delete(settings, "server_port") }, "server_port"},
		"non numeric port":    {func(settings map[string]any) { settings["server_port"] = "abc" }, "server_port"},
		"zero port":           {func(settings map[string]any) { settings["server_port"] = "0" }, "server_port"},
		"too large port":      {func(settings map[string]any) { settings["server_port"] = "65536" }, "server_port"},
		"fractional port":     {func(settings map[string]any) { settings["server_port"] = 443.5 }, "server_port"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			settings := valid()
			tc.mutate(settings)
			_, err := buildRealityInboundTLS(settings)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not contain %q", err, tc.want)
			}
		})
	}
}
