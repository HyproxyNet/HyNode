package singbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"

	box "github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/buf"
	singjson "github.com/sagernet/sing/common/json"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"

	"hynode/internal/certificate"
	appconfig "hynode/internal/config"
	"hynode/internal/panel"
	"hynode/internal/state"
)

type Factory struct {
	DataDir  string
	Fallback appconfig.CertificateConfig
}

type Runtime struct {
	instance *box.Box
}

func (f Factory) New(nodeID string, cfg panel.NodeConfig, users []panel.User, stats *state.Store) (*Runtime, error) {
	raw, err := f.build(nodeID, cfg, users)
	if err != nil {
		return nil, err
	}
	content, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	ctx := include.Context(context.Background())
	options, err := singjson.UnmarshalExtendedContext[option.Options](ctx, content)
	if err != nil {
		return nil, fmt.Errorf("validate sing-box configuration: %w", err)
	}
	instance, err := box.New(box.Options{Options: options, Context: ctx})
	if err != nil {
		return nil, fmt.Errorf("create sing-box: %w", err)
	}
	instance.Router().AppendTracker(&tracker{nodeID: nodeID, stats: stats})
	return &Runtime{instance: instance}, nil
}

func (r *Runtime) Start(_ context.Context) error { return r.instance.Start() }
func (r *Runtime) Close() error                  { return r.instance.Close() }

func (f Factory) build(nodeID string, cfg panel.NodeConfig, users []panel.User) (map[string]any, error) {
	if cfg.ServerPort <= 0 || cfg.ServerPort > 65535 {
		return nil, errors.New("invalid server_port")
	}
	if cfg.Plugin != "" || cfg.PluginOpts != "" {
		return nil, errors.New("shadowsocks plugin/plugin_opts is unsupported by the embedded sing-box inbound")
	}
	if cfg.Protocol == "vless" && cfg.Decryption != "" && cfg.Decryption != "none" {
		return nil, errors.New("vless decryption other than none is unsupported by the embedded sing-box inbound")
	}
	if cfg.Protocol == "tuic" && cfg.Version == 4 {
		return nil, errors.New("tuic version 4 token authentication is unsupported by the embedded sing-box inbound")
	}
	inbound, err := f.inbound(nodeID, cfg, users)
	if err != nil {
		return nil, err
	}
	result := map[string]any{
		"log":      map[string]any{"level": "info", "timestamp": true},
		"inbounds": []any{inbound},
		"outbounds": []any{
			map[string]any{"type": "direct", "tag": "direct"},
			map[string]any{"type": "block", "tag": "block"},
		},
		"route": map[string]any{"final": "direct"},
	}
	if len(cfg.CustomOutbounds) > 0 && string(cfg.CustomOutbounds) != "null" {
		var custom []any
		if err := json.Unmarshal(cfg.CustomOutbounds, &custom); err != nil {
			return nil, fmt.Errorf("custom_outbounds must be an array: %w", err)
		}
		result["outbounds"] = append(result["outbounds"].([]any), custom...)
	}
	var rules []any
	standard, err := compilePanelRoutes(cfg.Routes)
	if err != nil {
		return nil, err
	}
	rules = append(rules, standard...)
	if len(cfg.CustomRoutes) > 0 && string(cfg.CustomRoutes) != "null" {
		var custom []any
		if err := json.Unmarshal(cfg.CustomRoutes, &custom); err != nil {
			return nil, fmt.Errorf("custom_routes must be a sing-box rules array: %w", err)
		}
		rules = append(custom, rules...)
	}
	if len(rules) > 0 {
		result["route"].(map[string]any)["rules"] = rules
	}
	return result, nil
}

func (f Factory) inbound(nodeID string, cfg panel.NodeConfig, users []panel.User) (map[string]any, error) {
	listen := cfg.ListenIP
	if listen == "" {
		listen = "0.0.0.0"
	}
	protocol := cfg.Protocol
	if protocol == "hysteria" && cfg.Version == 2 {
		protocol = "hysteria2"
	}
	in := map[string]any{
		"type":        protocol,
		"tag":         "hyboard-" + nodeID,
		"listen":      listen,
		"listen_port": cfg.ServerPort,
	}
	addTransport(in, cfg)
	addMultiplex(in, cfg)
	switch protocol {
	case "shadowsocks":
		in["method"] = cfg.Cipher
		if cfg.ServerKey != "" {
			in["password"] = cfg.ServerKey
		}
		in["users"] = mapUsers(users, func(u panel.User) map[string]any {
			return map[string]any{"name": strconv.FormatInt(u.ID, 10), "password": u.UUID}
		})
	case "vmess":
		in["users"] = mapUsers(users, func(u panel.User) map[string]any {
			return map[string]any{"name": strconv.FormatInt(u.ID, 10), "uuid": u.UUID}
		})
	case "vless":
		in["users"] = mapUsers(users, func(u panel.User) map[string]any {
			return map[string]any{"name": strconv.FormatInt(u.ID, 10), "uuid": u.UUID, "flow": cfg.Flow}
		})
	case "trojan":
		in["users"] = passwordUsers(users)
	case "hysteria":
		in["up_mbps"], in["down_mbps"], in["obfs"] = cfg.UpMbps, cfg.DownMbps, cfg.Obfs
		in["users"] = mapUsers(users, func(u panel.User) map[string]any {
			return map[string]any{"name": strconv.FormatInt(u.ID, 10), "auth_str": u.UUID}
		})
	case "hysteria2":
		in["up_mbps"], in["down_mbps"] = cfg.UpMbps, cfg.DownMbps
		if value, ok := cfg.Obfs.(string); ok && value != "" {
			in["obfs"] = map[string]any{"type": value, "password": cfg.ObfsPassword}
		}
		in["users"] = passwordUsers(users)
	case "tuic":
		in["congestion_control"], in["auth_timeout"], in["zero_rtt_handshake"], in["heartbeat"] = cfg.Congestion, cfg.AuthTimeout, cfg.ZeroRTT, cfg.Heartbeat
		in["users"] = mapUsers(users, func(u panel.User) map[string]any {
			return map[string]any{"name": strconv.FormatInt(u.ID, 10), "uuid": u.UUID, "password": u.UUID}
		})
	case "anytls":
		in["padding_scheme"] = cfg.PaddingScheme
		in["users"] = passwordUsers(users)
	case "socks", "http", "naive":
		in["users"] = mapUsers(users, func(u panel.User) map[string]any {
			return map[string]any{"username": u.UUID, "password": u.UUID}
		})
	default:
		return nil, fmt.Errorf("unsupported sing-box protocol %q", cfg.Protocol)
	}
	if requiresTLS(protocol, cfg) {
		if cfg.TLS == 2 {
			reality := cloneMap(cfg.TLSSettings)
			if value, ok := reality["private_key"].(string); !ok || value == "" {
				return nil, errors.New("reality TLS requires tls_settings.private_key for the inbound")
			}
			reality["enabled"] = true
			in["tls"] = map[string]any{
				"enabled":     true,
				"server_name": cfg.ServerName,
				"reality":     reality,
			}
			return in, nil
		}
		material, err := certificate.Resolve(cfg.CertConfig, f.Fallback, f.DataDir, nodeID, cfg.ServerName)
		if err != nil {
			return nil, err
		}
		if material.Certificate == "" && material.CertificatePath == "" && material.Provider == nil {
			return nil, fmt.Errorf("%s requires TLS certificate configuration", protocol)
		}
		tls := map[string]any{"enabled": true, "server_name": cfg.ServerName}
		if material.Provider != nil {
			tls["certificate_provider"] = material.Provider
		} else if material.CertificatePath != "" {
			tls["certificate_path"], tls["key_path"] = material.CertificatePath, material.KeyPath
		} else {
			tls["certificate"], tls["key"] = []string{material.Certificate}, []string{material.Key}
		}
		in["tls"] = tls
	}
	return in, nil
}

func addMultiplex(in map[string]any, cfg panel.NodeConfig) {
	if len(cfg.Multiplex) == 0 {
		return
	}
	in["multiplex"] = cfg.Multiplex
}

func addTransport(in map[string]any, cfg panel.NodeConfig) {
	if cfg.Network == "" || cfg.Network == "tcp" {
		return
	}
	network := strings.ToLower(cfg.Network)
	if network == "ws" {
		network = "ws"
	}
	transport := map[string]any{"type": network}
	for key, value := range cfg.NetworkSettings {
		if key == "serviceName" {
			transport["service_name"] = value
		} else {
			transport[key] = value
		}
	}
	in["transport"] = transport
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
				domains = append(domains, item)
			}
		}
		if len(domains) > 0 {
			result = append(result, map[string]any{"domain_suffix": domains, "outbound": outbound})
		}
		if len(cidrs) > 0 {
			result = append(result, map[string]any{"ip_cidr": cidrs, "outbound": outbound})
		}
	}
	return result, nil
}

func requiresTLS(protocol string, cfg panel.NodeConfig) bool {
	return cfg.TLS > 0 || protocol == "trojan" || protocol == "hysteria" || protocol == "hysteria2" || protocol == "tuic" || protocol == "anytls" || protocol == "naive"
}

func cloneMap(input map[string]any) map[string]any {
	result := make(map[string]any, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}

func passwordUsers(users []panel.User) []map[string]any {
	return mapUsers(users, func(u panel.User) map[string]any {
		return map[string]any{"name": strconv.FormatInt(u.ID, 10), "password": u.UUID}
	})
}

func mapUsers(users []panel.User, build func(panel.User) map[string]any) []map[string]any {
	result := make([]map[string]any, 0, len(users))
	for _, user := range users {
		result = append(result, build(user))
	}
	return result
}

type tracker struct {
	nodeID string
	stats  *state.Store
}

func (t *tracker) RoutedConnection(_ context.Context, conn net.Conn, metadata adapter.InboundContext, _ adapter.Rule, _ adapter.Outbound) net.Conn {
	id, ok := t.resolveUser(metadata.User)
	if !ok || !t.stats.Open(t.nodeID, id, metadata.Source.Addr.String()) {
		_ = conn.Close()
		return conn
	}
	return state.WrapConn(conn, t.stats, t.nodeID, id)
}

func (t *tracker) RoutedPacketConnection(_ context.Context, conn N.PacketConn, metadata adapter.InboundContext, _ adapter.Rule, _ adapter.Outbound) N.PacketConn {
	id, ok := t.resolveUser(metadata.User)
	if !ok || !t.stats.Open(t.nodeID, id, metadata.Source.Addr.String()) {
		_ = conn.Close()
		return conn
	}
	return &trackedPacketConn{PacketConn: conn, stats: t.stats, nodeID: t.nodeID, userID: id}
}

func (t *tracker) resolveUser(name string) (int64, bool) {
	if id, err := strconv.ParseInt(name, 10, 64); err == nil {
		return id, true
	}
	return t.stats.UserBySecret(name)
}

type trackedPacketConn struct {
	N.PacketConn
	stats  *state.Store
	nodeID string
	userID int64
	once   sync.Once
}

func (c *trackedPacketConn) ReadPacket(buffer *buf.Buffer) (M.Socksaddr, error) {
	destination, err := c.PacketConn.ReadPacket(buffer)
	if buffer.Len() > 0 {
		c.stats.Wait(c.userID, buffer.Len())
		c.stats.Add(c.nodeID, c.userID, int64(buffer.Len()), 0)
	}
	return destination, err
}

func (c *trackedPacketConn) WritePacket(buffer *buf.Buffer, destination M.Socksaddr) error {
	n := buffer.Len()
	c.stats.Wait(c.userID, n)
	err := c.PacketConn.WritePacket(buffer, destination)
	if err == nil && n > 0 {
		c.stats.Add(c.nodeID, c.userID, 0, int64(n))
	}
	return err
}

func (c *trackedPacketConn) Close() error {
	err := c.PacketConn.Close()
	c.once.Do(func() { c.stats.Close(c.nodeID, c.userID) })
	return err
}
