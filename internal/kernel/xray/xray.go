// Package xray embeds Xray-core as a protocol kernel for HyNode.
//
// It is preferred over sing-box for protocols/transports Xray implements
// natively — especially VLESS/VMess/Trojan over XHTTP and REALITY — while
// sing-box remains the fallback for Hysteria, TUIC, AnyTLS, Naive and other
// protocols Xray does not support.
package xray

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/features/stats"
	"github.com/xtls/xray-core/infra/conf/serial"

	// Register Xray features and proxy handlers (mirrors main/distro/all
	// minus CLI/observatory/metrics extras we do not need).
	_ "github.com/xtls/xray-core/app/dispatcher"
	_ "github.com/xtls/xray-core/app/log"
	_ "github.com/xtls/xray-core/app/policy"
	_ "github.com/xtls/xray-core/app/proxyman/inbound"
	_ "github.com/xtls/xray-core/app/proxyman/outbound"
	_ "github.com/xtls/xray-core/app/router"
	_ "github.com/xtls/xray-core/app/stats"
	_ "github.com/xtls/xray-core/proxy/freedom"
	_ "github.com/xtls/xray-core/proxy/http"
	_ "github.com/xtls/xray-core/proxy/shadowsocks"
	_ "github.com/xtls/xray-core/proxy/shadowsocks_2022"
	_ "github.com/xtls/xray-core/proxy/socks"
	_ "github.com/xtls/xray-core/proxy/trojan"
	_ "github.com/xtls/xray-core/proxy/vless/inbound"
	_ "github.com/xtls/xray-core/proxy/vmess/inbound"
	_ "github.com/xtls/xray-core/transport/internet/grpc"
	_ "github.com/xtls/xray-core/transport/internet/httpupgrade"
	_ "github.com/xtls/xray-core/transport/internet/kcp"
	_ "github.com/xtls/xray-core/transport/internet/reality"
	_ "github.com/xtls/xray-core/transport/internet/splithttp"
	_ "github.com/xtls/xray-core/transport/internet/tagged/taggedimpl"
	_ "github.com/xtls/xray-core/transport/internet/tcp"
	_ "github.com/xtls/xray-core/transport/internet/tls"
	_ "github.com/xtls/xray-core/transport/internet/udp"
	_ "github.com/xtls/xray-core/transport/internet/websocket"

	"hynode/internal/certificate"
	appconfig "hynode/internal/config"
	"hynode/internal/panel"
	"hynode/internal/state"
)

// Factory builds Xray runtimes from panel node configuration.
type Factory struct {
	DataDir  string
	Fallback appconfig.CertificateConfig
}

// Runtime wraps a live Xray instance plus a stats poller that feeds
// HyNode's state.Store (traffic deltas and online IPs).
type Runtime struct {
	factory Factory
	nodeID  string
	cfg     panel.NodeConfig
	users   []panel.User
	stats   *state.Store

	mu       sync.Mutex
	instance *core.Instance

	stop     chan struct{}
	stopped  sync.Once
	pollDone chan struct{}

	// Cumulative Xray counters from the previous poll, keyed by user email.
	lastUp   map[string]int64
	lastDown map[string]int64
}

// Supported reports whether this kernel should handle the given node config.
// Protocols Xray does not implement (hysteria, tuic, anytls, naive, …) and
// transports Xray removed (h2) fall back to sing-box.
func Supported(cfg panel.NodeConfig) bool {
	switch cfg.Protocol {
	case "vless", "vmess", "trojan", "shadowsocks", "socks", "http":
	default:
		return false
	}
	if cfg.Protocol == "shadowsocks" && (cfg.Plugin != "" || cfg.PluginOpts != "") {
		return false // Xray has no SS plugin support
	}
	switch strings.ToLower(cfg.Network) {
	case "", "tcp", "ws", "websocket", "grpc", "httpupgrade", "xhttp", "splithttp", "kcp", "mkcp":
		return true
	case "h2", "h3", "http":
		// HTTP/2 transport was removed from Xray in favour of XHTTP.
		return false
	default:
		return false
	}
}

// New validates the config and creates a stopped Xray runtime.
func (f Factory) New(nodeID string, cfg panel.NodeConfig, users []panel.User, statsStore *state.Store) (*Runtime, error) {
	if !Supported(cfg) {
		return nil, fmt.Errorf("xray kernel does not support protocol=%q network=%q", cfg.Protocol, cfg.Network)
	}
	r := &Runtime{
		factory:  f,
		nodeID:   nodeID,
		cfg:      cfg,
		users:    users,
		stats:    statsStore,
		lastUp:   make(map[string]int64),
		lastDown: make(map[string]int64),
	}
	if err := r.buildInstance(users); err != nil {
		return nil, err
	}
	return r, nil
}

// Start launches the Xray instance and the stats poller.
func (r *Runtime) Start(_ context.Context) error {
	r.mu.Lock()
	inst := r.instance
	r.mu.Unlock()
	if inst == nil {
		return errors.New("xray runtime not built")
	}
	if err := inst.Start(); err != nil {
		return fmt.Errorf("start xray: %w", err)
	}
	r.stop = make(chan struct{})
	r.pollDone = make(chan struct{})
	go r.pollLoop(r.stop, r.pollDone)
	return nil
}

// Close stops the poller and shuts down Xray.
func (r *Runtime) Close() error {
	r.stopped.Do(func() {
		if r.stop != nil {
			close(r.stop)
			if r.pollDone != nil {
				<-r.pollDone
			}
		}
	})
	r.mu.Lock()
	inst := r.instance
	r.instance = nil
	r.mu.Unlock()
	if inst == nil {
		return nil
	}
	return inst.Close()
}

// ReloadUsers rebuilds the instance with an updated user list (hot reload).
func (r *Runtime) ReloadUsers(users []panel.User) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	prev := r.instance
	wasRunning := prev != nil && prev.IsRunning()

	next, err := r.buildInstanceLocked(users)
	if err != nil {
		return err
	}
	if wasRunning {
		if err := next.Start(); err != nil {
			_ = next.Close()
			return fmt.Errorf("start reloaded xray: %w", err)
		}
	}
	// Brief drain so in-flight handshakes on the old instance can finish.
	if prev != nil {
		_ = prev.Close()
		time.Sleep(100 * time.Millisecond)
	}
	r.instance = next
	r.users = users
	// Counter baselines belong to the previous instance.
	r.lastUp = make(map[string]int64)
	r.lastDown = make(map[string]int64)
	return nil
}

func (r *Runtime) buildInstance(users []panel.User) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, err := r.buildInstanceLocked(users)
	return err
}

func (r *Runtime) buildInstanceLocked(users []panel.User) (*core.Instance, error) {
	raw, err := r.factory.build(r.nodeID, r.cfg, users)
	if err != nil {
		return nil, err
	}
	content, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	pbConfig, err := serial.LoadJSONConfig(bytes.NewReader(content))
	if err != nil {
		return nil, fmt.Errorf("parse xray config: %w", err)
	}
	instance, err := core.New(pbConfig)
	if err != nil {
		return nil, fmt.Errorf("create xray: %w", err)
	}
	return instance, nil
}

// ─── Config builder ──────────────────────────────────────────────────────────

func (f Factory) build(nodeID string, cfg panel.NodeConfig, users []panel.User) (map[string]any, error) {
	if cfg.ServerPort <= 0 || cfg.ServerPort > 65535 {
		return nil, errors.New("invalid server_port")
	}
	if cfg.Plugin != "" || cfg.PluginOpts != "" {
		return nil, errors.New("shadowsocks plugin/plugin_opts is unsupported by the Xray kernel")
	}
	if cfg.Protocol == "vless" && cfg.Decryption != "" && cfg.Decryption != "none" {
		return nil, errors.New("vless decryption other than none is unsupported by the Xray kernel")
	}

	inbound, err := f.inbound(nodeID, cfg, users)
	if err != nil {
		return nil, err
	}

	result := map[string]any{
		"log":      map[string]any{"loglevel": "warning"},
		"inbounds": []any{inbound},
		"outbounds": []any{
			map[string]any{"protocol": "freedom", "tag": "direct"},
			map[string]any{"protocol": "blackhole", "tag": "block"},
		},
		// Enable per-user traffic/online stats used by the poller.
		"stats": map[string]any{},
		"policy": map[string]any{
			"levels": map[string]any{
				"0": map[string]any{
					"statsUserUplink":   true,
					"statsUserDownlink": true,
					"statsUserOnline":   true,
				},
			},
			"system": map[string]any{
				"statsInboundUplink":   true,
				"statsInboundDownlink": true,
			},
		},
	}

	if len(cfg.CustomOutbounds) > 0 && string(cfg.CustomOutbounds) != "null" {
		var custom []any
		if err := json.Unmarshal(cfg.CustomOutbounds, &custom); err != nil {
			return nil, fmt.Errorf("custom_outbounds must be an array: %w", err)
		}
		result["outbounds"] = append(result["outbounds"].([]any), custom...)
	}

	rules, err := compilePanelRoutes(cfg.Routes)
	if err != nil {
		return nil, err
	}
	if len(cfg.CustomRoutes) > 0 && string(cfg.CustomRoutes) != "null" {
		var custom []any
		if err := json.Unmarshal(cfg.CustomRoutes, &custom); err != nil {
			return nil, fmt.Errorf("custom_routes must be an array: %w", err)
		}
		// Same precedence as sing-box: custom rules are evaluated first.
		rules = append(custom, rules...)
	}
	if len(rules) > 0 {
		result["routing"] = map[string]any{"rules": rules}
	}

	_ = nodeID
	return result, nil
}

func (f Factory) inbound(nodeID string, cfg panel.NodeConfig, users []panel.User) (map[string]any, error) {
	listen := cfg.ListenIP
	if listen == "" {
		listen = "0.0.0.0"
	}

	in := map[string]any{
		"protocol": cfg.Protocol,
		"tag":      "hyboard-" + nodeID,
		"listen":   listen,
		"port":     cfg.ServerPort,
	}

	settings, err := protocolSettings(cfg, users)
	if err != nil {
		return nil, err
	}
	in["settings"] = settings

	stream, err := f.streamSettings(nodeID, cfg)
	if err != nil {
		return nil, err
	}
	if stream != nil {
		in["streamSettings"] = stream
	}
	return in, nil
}

func protocolSettings(cfg panel.NodeConfig, users []panel.User) (map[string]any, error) {
	switch cfg.Protocol {
	case "shadowsocks":
		// Multi-user 2022-blake3: server Key + per-user keys as password.
		// Classic AEAD: method + per-user password.
		method := cfg.Cipher
		settings := map[string]any{"method": method}
		is2022 := strings.Contains(method, "2022") || strings.Contains(method, "blake3")
		if is2022 {
			if cfg.ServerKey == "" {
				return nil, errors.New("shadowsocks 2022 requires server_key")
			}
			settings["method"] = method
			// Xray multi-user 2022 uses password on the server config as the key
			// and users carry their own keys — match sing-box layout:
			// server password = ServerKey, users password = UUID.
			settings["password"] = cfg.ServerKey
		}
		clients := make([]map[string]any, 0, len(users))
		for _, u := range users {
			clients = append(clients, map[string]any{
				"email":    strconv.FormatInt(u.ID, 10),
				"password": u.UUID,
			})
		}
		if is2022 {
			// For 2022 multi-user Xray expects users[].password = user key
			// and top-level password = server key (already set).
			settings["users"] = clients
		} else {
			// Classic: each user has method+password; no top-level password.
			delete(settings, "password")
			for i := range clients {
				clients[i]["method"] = method
			}
			settings["users"] = clients
		}
		return settings, nil

	case "vmess":
		clients := make([]map[string]any, 0, len(users))
		for _, u := range users {
			clients = append(clients, map[string]any{
				"id":       u.UUID,
				"email":    strconv.FormatInt(u.ID, 10),
				"security": "auto",
			})
		}
		return map[string]any{"clients": clients}, nil

	case "vless":
		clients := make([]map[string]any, 0, len(users))
		flow := cfg.Flow
		for _, u := range users {
			client := map[string]any{
				"id":    u.UUID,
				"email": strconv.FormatInt(u.ID, 10),
			}
			if flow != "" && flow != "none" {
				client["flow"] = flow
			}
			clients = append(clients, client)
		}
		decryption := cfg.Decryption
		if decryption == "" {
			decryption = "none"
		}
		return map[string]any{
			"clients":    clients,
			"decryption": decryption,
		}, nil

	case "trojan":
		clients := make([]map[string]any, 0, len(users))
		for _, u := range users {
			clients = append(clients, map[string]any{
				"password": u.UUID,
				"email":    strconv.FormatInt(u.ID, 10),
			})
		}
		return map[string]any{"clients": clients}, nil

	case "socks", "http":
		// Xray SOCKS/HTTP inbounds take accounts as [{user, pass}, …].
		// Username = UUID so UserBySecret can resolve stats email back to ID.
		accounts := make([]map[string]any, 0, len(users))
		for _, u := range users {
			accounts = append(accounts, map[string]any{
				"user": u.UUID,
				"pass": u.UUID,
			})
		}
		settings := map[string]any{"accounts": accounts}
		if cfg.Protocol == "socks" {
			settings["auth"] = "password"
			settings["udp"] = true
		}
		return settings, nil

	default:
		return nil, fmt.Errorf("unsupported xray protocol %q", cfg.Protocol)
	}
}

func (f Factory) streamSettings(nodeID string, cfg panel.NodeConfig) (map[string]any, error) {
	stream := map[string]any{}

	network := strings.ToLower(cfg.Network)
	switch network {
	case "", "tcp", "raw":
		stream["network"] = "tcp"
		if header, ok := cfg.NetworkSettings["header"]; ok && header != nil {
			stream["tcpSettings"] = map[string]any{"header": header}
		} else if len(cfg.NetworkSettings) > 0 {
			// acceptProxyProtocol etc.
			stream["tcpSettings"] = cfg.NetworkSettings
		}
	case "ws", "websocket":
		stream["network"] = "ws"
		stream["wsSettings"] = map[string]any(cfg.NetworkSettings)
	case "grpc":
		stream["network"] = "grpc"
		stream["grpcSettings"] = map[string]any(cfg.NetworkSettings)
	case "httpupgrade":
		stream["network"] = "httpupgrade"
		stream["httpupgradeSettings"] = map[string]any(cfg.NetworkSettings)
	case "kcp", "mkcp":
		stream["network"] = "kcp"
		stream["kcpSettings"] = map[string]any(cfg.NetworkSettings)
	case "xhttp", "splithttp":
		stream["network"] = "xhttp"
		xhttp, err := buildXHTTPSettings(cfg.NetworkSettings)
		if err != nil {
			return nil, err
		}
		stream["xhttpSettings"] = xhttp
	default:
		return nil, fmt.Errorf("unsupported xray network %q", cfg.Network)
	}

	// Security: 0=none, 1=TLS, 2=REALITY
	switch cfg.TLS {
	case 0:
		// leave stream["security"] unset → "none"
	case 1:
		stream["security"] = "tls"
		tls, err := f.buildTLS(nodeID, cfg)
		if err != nil {
			return nil, err
		}
		stream["tlsSettings"] = tls
	case 2:
		stream["security"] = "reality"
		reality, err := buildRealitySettings(cfg.TLSSettings)
		if err != nil {
			return nil, err
		}
		stream["realitySettings"] = reality
	default:
		return nil, fmt.Errorf("invalid tls mode %d", cfg.TLS)
	}

	// Panel multiplex → Xray mux (best-effort; Xray mux is outbound-oriented
	// but inbound accepts mux.cool when enabled on stream via mux field).
	if len(cfg.Multiplex) > 0 {
		if enabled, ok := cfg.Multiplex["enabled"].(bool); ok && enabled {
			mux := map[string]any{"enabled": true}
			if c, ok := cfg.Multiplex["concurrency"]; ok {
				mux["concurrency"] = c
			}
			// Xray expects mux at inbound top-level for some versions; place
			// both on streamSettings consumers that read it and top-level.
			// InboundDetourConfig has no mux — mux lives on outbound only.
			// Client-initiated mux still works without server mux settings.
			_ = mux
		}
	}

	return stream, nil
}

func (f Factory) buildTLS(nodeID string, cfg panel.NodeConfig) (map[string]any, error) {
	material, err := certificate.Resolve(cfg.CertConfig, f.Fallback, f.DataDir, nodeID, cfg.ServerName)
	if err != nil {
		return nil, err
	}
	if material.Certificate == "" && material.CertificatePath == "" && material.Provider == nil {
		return nil, fmt.Errorf("%s requires TLS certificate configuration", cfg.Protocol)
	}
	tls := map[string]any{}
	if cfg.ServerName != "" {
		tls["serverName"] = cfg.ServerName
	}
	cert := map[string]any{}
	switch {
	case material.CertificatePath != "":
		cert["certificateFile"] = material.CertificatePath
		cert["keyFile"] = material.KeyPath
	case material.Certificate != "":
		cert["certificate"] = []string{material.Certificate}
		cert["key"] = []string{material.Key}
	default:
		// ACME provider mode is sing-box specific; require file/content for Xray.
		return nil, errors.New("xray kernel TLS requires file or content certificate (ACME provider mode unsupported)")
	}
	tls["certificates"] = []any{cert}
	return tls, nil
}

func buildRealitySettings(settings map[string]any) (map[string]any, error) {
	if settings == nil {
		return nil, errors.New("reality TLS requires tls_settings")
	}
	serverName := strings.TrimSpace(stringSetting(settings, "server_name"))
	if serverName == "" {
		return nil, errors.New("reality TLS requires tls_settings.server_name")
	}
	privateKey := strings.TrimSpace(stringSetting(settings, "private_key"))
	if privateKey == "" {
		return nil, errors.New("reality TLS requires tls_settings.private_key")
	}
	serverPort, err := realityServerPort(settings["server_port"])
	if err != nil {
		return nil, err
	}
	shortID := strings.TrimSpace(stringSetting(settings, "short_id"))
	// Xray requires a non-empty shortIds array; "" is a valid entry meaning
	// "accept clients with empty short id" only when listed — panel always
	// generates an 8-hex id, but tolerate empty by emitting [""].
	shortIDs := []string{shortID}

	return map[string]any{
		"dest":        fmt.Sprintf("%s:%d", serverName, serverPort),
		"serverNames": []string{serverName},
		"privateKey":  privateKey,
		"shortIds":    shortIDs,
	}, nil
}

func buildXHTTPSettings(ns map[string]any) (map[string]any, error) {
	if ns == nil {
		return map[string]any{"mode": "auto"}, nil
	}
	// Panel template nests advanced options under "extra"; Xray's
	// SplitHTTPConfig expects them at the top level alongside path/host/mode.
	out := map[string]any{}
	for k, v := range ns {
		if k == "extra" {
			if extra, ok := v.(map[string]any); ok {
				for ek, ev := range extra {
					out[ek] = ev
				}
			}
			continue
		}
		out[k] = v
	}
	if _, ok := out["mode"]; !ok {
		out["mode"] = "auto"
	}
	// Validate mode values Xray accepts.
	switch strings.ToLower(fmt.Sprint(out["mode"])) {
	case "auto", "packet-up", "stream-up", "stream-one":
	default:
		return nil, fmt.Errorf("invalid xhttp mode %v", out["mode"])
	}
	return out, nil
}

func compilePanelRoutes(routes []panel.Route) ([]any, error) {
	var result []any
	for _, route := range routes {
		outbound := ""
		switch strings.ToLower(route.Action) {
		case "direct":
			outbound = "direct"
		case "block", "reject":
			outbound = "block"
		case "proxy":
			outbound = route.ActionValue
			if outbound == "" {
				return nil, fmt.Errorf("route %d proxy action requires action_value outbound tag", route.ID)
			}
		default:
			return nil, fmt.Errorf("route %d has unsupported action %q", route.ID, route.Action)
		}
		var domains, cidrs []string
		for _, item := range route.Match {
			item = strings.TrimSpace(strings.TrimPrefix(item, "*."))
			if item == "" {
				continue
			}
			if strings.Contains(item, "/") {
				cidrs = append(cidrs, item)
			} else {
				// domain: matches the domain and its subdomains (panel domain_suffix).
				domains = append(domains, "domain:"+item)
			}
		}
		if len(domains) > 0 {
			result = append(result, map[string]any{
				"type":        "field",
				"domain":      domains,
				"outboundTag": outbound,
			})
		}
		if len(cidrs) > 0 {
			result = append(result, map[string]any{
				"type":        "field",
				"ip":          cidrs,
				"outboundTag": outbound,
			})
		}
	}
	return result, nil
}

func stringSetting(settings map[string]any, key string) string {
	if settings == nil {
		return ""
	}
	value, ok := settings[key].(string)
	if !ok {
		return ""
	}
	return value
}

func realityServerPort(value any) (int, error) {
	switch v := value.(type) {
	case string:
		text := strings.TrimSpace(v)
		if text == "" {
			return 0, errors.New("reality TLS requires tls_settings.server_port for the inbound")
		}
		port, err := strconv.Atoi(text)
		if err != nil {
			return 0, fmt.Errorf("reality TLS tls_settings.server_port must be a valid port: %w", err)
		}
		return validateRealityServerPort(port)
	case int:
		return validateRealityServerPort(v)
	case int64:
		if v < 1 || v > 65535 {
			return 0, fmt.Errorf("reality TLS tls_settings.server_port out of range: %d", v)
		}
		return int(v), nil
	case float64:
		if math.Trunc(v) != v {
			return 0, fmt.Errorf("reality TLS tls_settings.server_port must be an integer: %v", v)
		}
		if v < 1 || v > 65535 {
			return 0, fmt.Errorf("reality TLS tls_settings.server_port out of range: %v", v)
		}
		return int(v), nil
	default:
		return 0, errors.New("reality TLS requires tls_settings.server_port for the inbound")
	}
}

func validateRealityServerPort(port int) (int, error) {
	if port < 1 || port > 65535 {
		return 0, fmt.Errorf("reality TLS tls_settings.server_port out of range: %d", port)
	}
	return port, nil
}

// ─── Stats poller ────────────────────────────────────────────────────────────

// pollInterval is how often Xray counters are drained into state.Store.
const pollInterval = 2 * time.Second

func (r *Runtime) pollLoop(stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			r.drainStats()
			return
		case <-ticker.C:
			r.drainStats()
		}
	}
}

func (r *Runtime) drainStats() {
	r.mu.Lock()
	inst := r.instance
	users := r.users
	r.mu.Unlock()
	if inst == nil || !inst.IsRunning() {
		return
	}
	sm, ok := inst.GetFeature(stats.ManagerType()).(stats.Manager)
	if !ok || sm == nil {
		return
	}

	for _, u := range users {
		// vless/vmess/trojan/ss use numeric ID as Xray email; socks/http use UUID.
		emails := []string{strconv.FormatInt(u.ID, 10)}
		if u.UUID != "" && u.UUID != emails[0] {
			emails = append(emails, u.UUID)
		}
		var upDelta, downDelta int64
		var matched bool
		var onlineIPs []string

		for _, email := range emails {
			upName := "user>>>" + email + ">>>traffic>>>uplink"
			downName := "user>>>" + email + ">>>traffic>>>downlink"
			onlineName := "user>>>" + email + ">>>online"

			if c := sm.GetCounter(upName); c != nil {
				v := c.Value()
				if last, ok := r.lastUp[email]; ok && v >= last {
					upDelta += v - last
				}
				r.lastUp[email] = v
				matched = true
			}
			if c := sm.GetCounter(downName); c != nil {
				v := c.Value()
				if last, ok := r.lastDown[email]; ok && v >= last {
					downDelta += v - last
				}
				r.lastDown[email] = v
				matched = true
			}
			if om := sm.GetOnlineMap(onlineName); om != nil {
				om.ForEach(func(ip string, _ int64) bool {
					onlineIPs = append(onlineIPs, ip)
					return true
				})
				matched = true
			}
		}
		if !matched {
			continue
		}
		if upDelta > 0 || downDelta > 0 {
			r.stats.Add(r.nodeID, u.ID, upDelta, downDelta)
		}
		// Device limits are not enforced here (no accept-time hook in Xray);
		// online IPs are reported for Alive/Online only.
		r.stats.SyncAlive(r.nodeID, u.ID, onlineIPs)
	}
}
