package certificate

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"time"

	"hynode/internal/config"
)

type Material struct {
	Certificate     string
	Key             string
	CertificatePath string
	KeyPath         string
	Provider        map[string]any
}

func Resolve(raw map[string]any, fallback config.CertificateConfig, dataDir, nodeID, serverName string) (Material, error) {
	selected := fallback
	if len(raw) > 0 {
		selected = fromMap(raw)
	}
	switch strings.ToLower(selected.Mode) {
	case "", "none":
		return Material{}, nil
	case "file":
		if selected.CertificatePath == "" || selected.KeyPath == "" {
			return Material{}, errors.New("certificate file mode requires certificate_path and key_path")
		}
		return Material{CertificatePath: selected.CertificatePath, KeyPath: selected.KeyPath}, nil
	case "content":
		if selected.Certificate == "" || selected.Key == "" {
			return Material{}, errors.New("certificate content mode requires cert_content and cert_key")
		}
		return Material{Certificate: selected.Certificate, Key: selected.Key}, nil
	case "self-signed":
		name := selected.ServerName
		if name == "" {
			name = serverName
		}
		if name == "" {
			name = "localhost"
		}
		return generateSelfSigned(filepath.Join(dataDir, nodeID, "tls"), name)
	case "http-01":
		name := selected.ServerName
		if name == "" {
			name = serverName
		}
		if name == "" {
			return Material{}, errors.New("ACME HTTP-01 mode requires cert_domain")
		}
		provider := map[string]any{
			"type":                "acme",
			"domain":              []string{name},
			"default_server_name": name,
			"data_directory":      filepath.Join(dataDir, nodeID, "acme"),
		}
		if selected.Email != "" {
			provider["email"] = selected.Email
		}
		return Material{Provider: provider}, nil
	case "dns-01":
		name := selected.ServerName
		if name == "" {
			name = serverName
		}
		if name == "" {
			return Material{}, errors.New("ACME DNS-01 mode requires cert_domain")
		}
		if selected.Provider == "" {
			return Material{}, errors.New("ACME DNS-01 mode requires cert_dns_provider")
		}
		challenge := map[string]any{"provider": strings.ToLower(selected.Provider)}
		envs := parseEnvs(selected.Envs)
		switch strings.ToLower(selected.Provider) {
		case "cloudflare":
			token := envs["CF_API_TOKEN"]
			if token == "" {
				token = envs["CLOUDFLARE_API_TOKEN"]
			}
			if token == "" {
				return Material{}, errors.New("ACME cloudflare requires CF_API_TOKEN in cert_envs")
			}
			challenge["api_token"] = token
		case "alidns":
			keyID := envs["ALICLOUD_ACCESS_KEY"]
			secret := envs["ALICLOUD_SECRET_KEY"]
			if keyID == "" || secret == "" {
				return Material{}, errors.New("ACME alidns requires ALICLOUD_ACCESS_KEY and ALICLOUD_SECRET_KEY in cert_envs")
			}
			challenge["access_key_id"] = keyID
			challenge["access_key_secret"] = secret
		default:
			return Material{}, fmt.Errorf("unsupported ACME DNS provider %q (supported: cloudflare, alidns)", selected.Provider)
		}
		provider := map[string]any{
			"type":                   "acme",
			"domain":                 []string{name},
			"default_server_name":    name,
			"data_directory":         filepath.Join(dataDir, nodeID, "acme"),
			"disable_http_challenge": true,
			"dns01_challenge":        challenge,
		}
		if selected.Email != "" {
			provider["email"] = selected.Email
		}
		return Material{Provider: provider}, nil
	default:
		return Material{}, fmt.Errorf("unsupported certificate mode %q (supported: none, self-signed, http-01, dns-01, content, file)", selected.Mode)
	}
}

// fromMap converts the panel's cert_config JSON map into CertificateConfig,
// using the exact field names from HyBoard-Admin-Source.
func fromMap(raw map[string]any) config.CertificateConfig {
	value := func(keys ...string) string {
		for _, key := range keys {
			if item, ok := raw[key].(string); ok {
				return item
			}
		}
		return ""
	}
	return config.CertificateConfig{
		Mode:            value("cert_mode"),
		CertificatePath: value("certificate_path", "cert_path"),
		KeyPath:         value("key_path"),
		Certificate:     value("cert_content", "certificate"),
		Key:             value("cert_key", "key"),
		ServerName:      value("cert_domain", "server_name", "domain"),
		Provider:        value("cert_dns_provider", "provider"),
		Email:           value("cert_email", "email"),
		Envs:            value("cert_envs"),
	}
}

// parseEnvs parses the cert_envs string from the panel.
// Expected format: "KEY1=value1\nKEY2=value2" or JSON object.
func parseEnvs(raw string) map[string]string {
	result := make(map[string]string)
	if raw == "" {
		return result
	}
	// Try JSON first
	var obj map[string]string
	if err := json.Unmarshal([]byte(raw), &obj); err == nil {
		return obj
	}
	// Fall back to KEY=value lines
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if idx := strings.Index(line, "="); idx > 0 {
			result[strings.TrimSpace(line[:idx])] = strings.TrimSpace(line[idx+1:])
		}
	}
	return result
}

func generateSelfSigned(dir, serverName string) (Material, error) {
	certPath := filepath.Join(dir, "cert.pem")
	keyPath := filepath.Join(dir, "key.pem")
	if _, err := os.Stat(certPath); err == nil {
		if _, err = os.Stat(keyPath); err == nil {
			return Material{CertificatePath: certPath, KeyPath: keyPath}, nil
		}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Material{}, err
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return Material{}, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return Material{}, err
	}
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: serverName},
		DNSNames:     []string{serverName},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(10 * 365 * 24 * time.Hour), // 10 years, matching admin description
		KeyUsage:     x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return Material{}, err
	}
	certBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyBytes := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	if err = os.WriteFile(certPath, certBytes, 0o600); err != nil {
		return Material{}, err
	}
	if err = os.WriteFile(keyPath, keyBytes, 0o600); err != nil {
		return Material{}, err
	}
	return Material{CertificatePath: certPath, KeyPath: keyPath}, nil
}
