package mieru

import (
	"net"
	"testing"

	"github.com/enfein/mieru/v3/apis/model"

	"hynode/internal/panel"
)

func TestCompileBlockRoutes(t *testing.T) {
	rules, err := compileRoutes([]panel.Route{{ID: 1, Match: []string{"*.example.com", "10.0.0.0/8"}, Action: "block"}})
	if err != nil {
		t.Fatal(err)
	}
	runtime := Runtime{blocks: rules}
	if !runtime.blocked(model.AddrSpec{FQDN: "api.example.com"}) {
		t.Fatal("domain was not blocked")
	}
	if !runtime.blocked(model.AddrSpec{IP: net.ParseIP("10.1.2.3")}) {
		t.Fatal("CIDR was not blocked")
	}
}

func TestMieruRejectsProxyRoute(t *testing.T) {
	_, err := compileRoutes([]panel.Route{{ID: 2, Match: []string{"example.com"}, Action: "proxy", ActionValue: "out"}})
	if err == nil {
		t.Fatal("expected unsupported proxy route error")
	}
}
