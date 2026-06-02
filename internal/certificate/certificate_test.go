package certificate

import (
	"testing"

	"hynode/internal/config"
)

func TestResolveACMEDNSCloudflare(t *testing.T) {
	material, err := Resolve(nil, config.CertificateConfig{
		Mode:       "dns-01",
		ServerName: "edge.example.com",
		Provider:   "cloudflare",
		Envs:       "CF_API_TOKEN=mytoken",
	}, t.TempDir(), "node-a", "")
	if err != nil {
		t.Fatal(err)
	}
	if material.Provider["type"] != "acme" {
		t.Fatalf("provider = %#v", material.Provider)
	}
}

func TestResolveSelfSigned(t *testing.T) {
	material, err := Resolve(nil, config.CertificateConfig{Mode: "self-signed"}, t.TempDir(), "node-a", "localhost")
	if err != nil {
		t.Fatal(err)
	}
	if material.CertificatePath == "" || material.KeyPath == "" {
		t.Fatalf("material = %#v", material)
	}
}

func TestResolveContent(t *testing.T) {
	material, err := Resolve(nil, config.CertificateConfig{
		Mode:        "content",
		Certificate: "CERT",
		Key:         "KEY",
	}, t.TempDir(), "node-a", "")
	if err != nil {
		t.Fatal(err)
	}
	if material.Certificate != "CERT" || material.Key != "KEY" {
		t.Fatalf("material = %#v", material)
	}
}

func TestFromMapFrontendFields(t *testing.T) {
	raw := map[string]any{
		"cert_mode":         "self-signed",
		"cert_domain":       "example.com",
		"cert_email":        "admin@example.com",
		"cert_dns_provider": "cloudflare",
		"cert_envs":         "CF_API_TOKEN=abc123",
	}
	cfg := fromMap(raw)
	if cfg.Mode != "self-signed" {
		t.Fatalf("mode = %q", cfg.Mode)
	}
	if cfg.ServerName != "example.com" {
		t.Fatalf("server_name = %q", cfg.ServerName)
	}
	if cfg.Email != "admin@example.com" {
		t.Fatalf("email = %q", cfg.Email)
	}
	if cfg.Provider != "cloudflare" {
		t.Fatalf("provider = %q", cfg.Provider)
	}
	if cfg.Envs != "CF_API_TOKEN=abc123" {
		t.Fatalf("envs = %q", cfg.Envs)
	}
}

func TestParseEnvs(t *testing.T) {
	// Test KEY=value format
	envs := parseEnvs("CF_API_TOKEN=abc123\nCF_ZONE_ID=zone456")
	if envs["CF_API_TOKEN"] != "abc123" {
		t.Fatalf("CF_API_TOKEN = %q", envs["CF_API_TOKEN"])
	}
	if envs["CF_ZONE_ID"] != "zone456" {
		t.Fatalf("CF_ZONE_ID = %q", envs["CF_ZONE_ID"])
	}

	// Test JSON format
	envs = parseEnvs(`{"CF_API_TOKEN":"xyz"}`)
	if envs["CF_API_TOKEN"] != "xyz" {
		t.Fatalf("JSON CF_API_TOKEN = %q", envs["CF_API_TOKEN"])
	}
}
