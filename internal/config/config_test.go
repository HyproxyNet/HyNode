package config

import (
	"os"
	"testing"
)

func TestLoadEnvironmentQuickStart(t *testing.T) {
	t.Setenv("HYBOARD_URL", "https://panel.example")
	t.Setenv("HYBOARD_TOKEN", "token")
	t.Setenv("HYNODE_NODE_ID", "edge-a")
	cfg, _, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Nodes) != 1 || cfg.Nodes[0].ID != "edge-a" {
		t.Fatalf("nodes = %#v", cfg.Nodes)
	}
	if cfg.Runtime.DataDir == "" {
		t.Fatal("missing data dir")
	}
	_ = os.Unsetenv("HYNODE_CONFIG")
}
