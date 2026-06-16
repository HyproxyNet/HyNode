package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Panel               PanelConfig       `yaml:"panel"`
	Nodes               []NodeConfig      `yaml:"nodes"`
	Runtime             RuntimeConfig     `yaml:"runtime"`
	Sync                SyncConfig        `yaml:"sync"`
	CertificateFallback CertificateConfig `yaml:"certificate_fallback"`
}

type PanelConfig struct {
	URL      string `yaml:"url"`
	Token    string `yaml:"token"`
	TokenEnv string `yaml:"token_env"`
}

type NodeConfig struct {
	// ID is the node identifier sent as the "node-id" header to HyBoard.
	// Recommended: use the node's custom code (string) for unambiguous resolution.
	// Numeric values are also accepted but may match both the auto-increment id
	// and a numeric code, which depends on database ordering.
	ID      string `yaml:"id"`
	Enabled *bool  `yaml:"enabled"`
}

func (n NodeConfig) IsEnabled() bool { return n.Enabled == nil || *n.Enabled }

type RuntimeConfig struct {
	DataDir      string `yaml:"data_dir"`
	LogLevel     string `yaml:"log_level"`
	HealthListen string `yaml:"health_listen"`
}

type SyncConfig struct {
	PullInterval   time.Duration `yaml:"-"`
	ReportInterval time.Duration `yaml:"-"`
	PullRaw        string        `yaml:"pull_interval"`
	ReportRaw      string        `yaml:"report_interval"`
}

type CertificateConfig struct {
	Mode            string `yaml:"cert_mode" json:"cert_mode"`
	CertificatePath string `yaml:"certificate_path" json:"certificate_path"`
	KeyPath         string `yaml:"key_path" json:"key_path"`
	Certificate     string `yaml:"certificate" json:"certificate"`
	Key             string `yaml:"key" json:"key"`
	ServerName      string `yaml:"server_name" json:"server_name"`
	Provider        string `yaml:"provider" json:"provider"`
	Email           string `yaml:"email" json:"email"`
	Envs            string `yaml:"envs" json:"envs"`
}

func Load(path string) (Config, []string, error) {
	var cfg Config
	if path == "" {
		path = os.Getenv("HYNODE_CONFIG")
	}
	if path != "" {
		content, err := os.ReadFile(path)
		if err != nil {
			return cfg, nil, err
		}
		if err = yaml.Unmarshal(content, &cfg); err != nil {
			return cfg, nil, fmt.Errorf("parse YAML: %w", err)
		}
	}
	applyEnvironment(&cfg)
	if cfg.Runtime.DataDir == "" {
		cfg.Runtime.DataDir = "./data"
	}
	if cfg.Runtime.LogLevel == "" {
		cfg.Runtime.LogLevel = "info"
	}
	if cfg.Runtime.HealthListen == "" {
		cfg.Runtime.HealthListen = "127.0.0.1:9090"
	}
	if cfg.Sync.PullRaw != "" {
		d, err := time.ParseDuration(cfg.Sync.PullRaw)
		if err != nil {
			return cfg, nil, fmt.Errorf("sync.pull_interval: %w", err)
		}
		cfg.Sync.PullInterval = d
	}
	if cfg.Sync.ReportRaw != "" {
		d, err := time.ParseDuration(cfg.Sync.ReportRaw)
		if err != nil {
			return cfg, nil, fmt.Errorf("sync.report_interval: %w", err)
		}
		cfg.Sync.ReportInterval = d
	}
	warnings, validationErr := cfg.Validate()
	if validationErr != nil {
		return cfg, nil, validationErr
	}
	absolute, err := filepath.Abs(cfg.Runtime.DataDir)
	if err != nil {
		return cfg, nil, err
	}
	cfg.Runtime.DataDir = absolute
	return cfg, warnings, nil
}

func applyEnvironment(cfg *Config) {
	if v := os.Getenv("HYBOARD_URL"); v != "" {
		cfg.Panel.URL = v
	}
	if v := os.Getenv("HYBOARD_TOKEN"); v != "" {
		cfg.Panel.Token = v
	}
	if cfg.Panel.Token == "" && cfg.Panel.TokenEnv != "" {
		cfg.Panel.Token = os.Getenv(cfg.Panel.TokenEnv)
	}
	if v := os.Getenv("HYNODE_NODE_ID"); v != "" && len(cfg.Nodes) == 0 {
		cfg.Nodes = []NodeConfig{{ID: v}}
	}
}

// Validate checks the configuration for errors. It returns warnings (non-fatal)
// and an error (fatal).
func (c Config) Validate() ([]string, error) {
	var warnings []string
	if !strings.HasPrefix(c.Panel.URL, "http://") && !strings.HasPrefix(c.Panel.URL, "https://") {
		return nil, errors.New("panel.url must start with http:// or https://")
	}
	if c.Panel.Token == "" {
		return nil, errors.New("panel.token is required")
	}
	seen := map[string]bool{}
	var enabled int
	for _, node := range c.Nodes {
		if !node.IsEnabled() {
			continue
		}
		enabled++
		if node.ID == "" {
			return nil, errors.New("enabled node id is required")
		}
		if isNumeric(node.ID) {
			warnings = append(warnings, fmt.Sprintf("node %q uses a numeric id; consider using the node's custom code for unambiguous panel resolution", node.ID))
		}
		if seen[node.ID] {
			return nil, fmt.Errorf("duplicate node id %q", node.ID)
		}
		seen[node.ID] = true
	}
	if enabled == 0 {
		return nil, errors.New("at least one enabled node is required")
	}
	if c.Sync.PullInterval < 0 || c.Sync.ReportInterval < 0 {
		return nil, errors.New("sync intervals must not be negative")
	}
	return warnings, nil
}

func isNumeric(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return len(s) > 0
}
