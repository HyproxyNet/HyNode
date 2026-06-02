package singbox

import (
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
