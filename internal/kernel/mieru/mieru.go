package mieru

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	apicommon "github.com/enfein/mieru/v3/apis/common"
	"github.com/enfein/mieru/v3/apis/constant"
	"github.com/enfein/mieru/v3/apis/model"
	mierserver "github.com/enfein/mieru/v3/apis/server"
	"github.com/enfein/mieru/v3/apis/trafficpattern"
	"github.com/enfein/mieru/v3/pkg/appctl/appctlpb"
	"github.com/enfein/mieru/v3/pkg/socks5"
	"google.golang.org/protobuf/proto"

	"hynode/internal/panel"
	"hynode/internal/state"
)

type Factory struct{}

type Runtime struct {
	nodeID string
	server mierserver.Server
	stats  *state.Store
	stop   chan struct{}
	wg     sync.WaitGroup
	blocks []blockRule
}

type blockRule struct {
	domains []string
	cidrs   []netip.Prefix
}

func (Factory) New(nodeID string, cfg panel.NodeConfig, users []panel.User, stats *state.Store) (*Runtime, error) {
	if cfg.ServerPort < 1 || cfg.ServerPort > 65535 {
		return nil, fmt.Errorf("invalid mieru server_port %d", cfg.ServerPort)
	}
	if len(cfg.CustomOutbounds) > 0 && string(cfg.CustomOutbounds) != "null" ||
		len(cfg.CustomRoutes) > 0 && string(cfg.CustomRoutes) != "null" {
		return nil, fmt.Errorf("mieru does not support custom outbound or custom route configuration in this release")
	}
	blocks, err := compileRoutes(cfg.Routes)
	if err != nil {
		return nil, err
	}
	protocol := appctlpb.TransportProtocol_TCP.Enum()
	switch strings.ToUpper(cfg.Transport) {
	case "", "TCP":
	case "UDP":
		protocol = appctlpb.TransportProtocol_UDP.Enum()
	default:
		return nil, fmt.Errorf("invalid mieru transport %q", cfg.Transport)
	}
	serverCfg := &appctlpb.ServerConfig{
		PortBindings: []*appctlpb.PortBinding{{
			Port:     proto.Int32(int32(cfg.ServerPort)),
			Protocol: protocol,
		}},
	}
	for _, user := range users {
		serverCfg.Users = append(serverCfg.Users, &appctlpb.User{
			Name:     proto.String(user.UUID),
			Password: proto.String(user.UUID),
		})
	}
	if cfg.TrafficPattern != "" {
		pattern, err := trafficpattern.Decode(cfg.TrafficPattern)
		if err != nil {
			return nil, fmt.Errorf("decode mieru traffic_pattern: %w", err)
		}
		serverCfg.TrafficPattern = pattern
	}
	s := mierserver.NewServer()
	if err := s.Store(&mierserver.ServerConfig{Config: serverCfg}); err != nil {
		return nil, fmt.Errorf("store mieru config: %w", err)
	}
	return &Runtime{nodeID: nodeID, server: s, stats: stats, stop: make(chan struct{}), blocks: blocks}, nil
}

func (r *Runtime) Start(_ context.Context) error {
	if err := r.server.Start(); err != nil {
		return err
	}
	r.wg.Add(1)
	go r.accept()
	return nil
}

func (r *Runtime) Close() error {
	close(r.stop)
	err := r.server.Stop()
	r.wg.Wait()
	return err
}

func (r *Runtime) accept() {
	defer r.wg.Done()
	var consecutiveErrors int
	for {
		conn, req, err := r.server.Accept()
		if err != nil {
			select {
			case <-r.stop:
				return
			default:
				consecutiveErrors++
				if consecutiveErrors > 10 {
					// Backoff on persistent errors to avoid CPU spin.
					time.Sleep(time.Duration(min(consecutiveErrors, 100)) * 10 * time.Millisecond)
				}
				continue
			}
		}
		consecutiveErrors = 0
		r.wg.Add(1)
		go func() {
			defer r.wg.Done()
			r.handle(conn, req)
		}()
	}
}

func (r *Runtime) handle(proxyConn net.Conn, req *model.Request) {
	userContext, ok := proxyConn.(apicommon.UserContext)
	if !ok {
		_ = proxyConn.Close()
		return
	}
	userID, ok := r.stats.UserBySecret(userContext.UserName())
	if !ok || !r.stats.Open(r.nodeID, userID, state.IPFromAddr(proxyConn.RemoteAddr())) {
		_ = proxyConn.Close()
		return
	}
	conn := state.WrapConn(proxyConn, r.stats, r.nodeID, userID)
	defer conn.Close()
	switch req.Command {
	case constant.Socks5ConnectCmd:
		if r.blocked(req.DstAddr) {
			return
		}
		r.handleTCP(conn, req)
	case constant.Socks5UDPAssociateCmd:
		if len(r.blocks) > 0 {
			return
		}
		r.handleUDP(conn)
	}
}

func compileRoutes(routes []panel.Route) ([]blockRule, error) {
	var result []blockRule
	for _, route := range routes {
		switch strings.ToLower(route.Action) {
		case "direct":
			continue
		case "block", "reject":
		default:
			return nil, fmt.Errorf("mieru route %d action %q is unsupported; only direct and block are accepted", route.ID, route.Action)
		}
		var rule blockRule
		for _, raw := range route.Match {
			value := strings.TrimSpace(strings.TrimPrefix(raw, "*."))
			if value == "" {
				continue
			}
			if strings.Contains(value, "/") {
				prefix, err := netip.ParsePrefix(value)
				if err != nil {
					return nil, fmt.Errorf("mieru route %d CIDR %q: %w", route.ID, value, err)
				}
				rule.cidrs = append(rule.cidrs, prefix)
			} else {
				rule.domains = append(rule.domains, strings.ToLower(value))
			}
		}
		if len(rule.domains) > 0 || len(rule.cidrs) > 0 {
			result = append(result, rule)
		}
	}
	return result, nil
}

func (r *Runtime) blocked(destination model.AddrSpec) bool {
	host := strings.ToLower(destination.FQDN)
	var ip netip.Addr
	if len(destination.IP) > 0 {
		ip, _ = netip.AddrFromSlice(destination.IP)
		ip = ip.Unmap()
	}
	for _, rule := range r.blocks {
		for _, domain := range rule.domains {
			if host == domain || strings.HasSuffix(host, "."+domain) {
				return true
			}
		}
		for _, prefix := range rule.cidrs {
			if ip.IsValid() && prefix.Contains(ip) {
				return true
			}
		}
	}
	return false
}

func (r *Runtime) handleTCP(proxyConn net.Conn, req *model.Request) {
	target, err := net.Dial("tcp", req.DstAddr.String())
	if err != nil {
		return
	}
	defer target.Close()
	local, _ := target.LocalAddr().(*net.TCPAddr)
	if local == nil {
		return
	}
	response := &model.Response{
		Reply:    constant.Socks5ReplySuccess,
		BindAddr: model.AddrSpec{IP: local.IP, Port: local.Port},
	}
	if response.WriteToSocks5(proxyConn) != nil {
		return
	}
	copyBoth(proxyConn, target)
}

func (r *Runtime) handleUDP(proxyConn net.Conn) {
	udpConn, err := net.ListenUDP("udp", nil)
	if err != nil {
		return
	}
	defer udpConn.Close()
	_, port, err := net.SplitHostPort(udpConn.LocalAddr().String())
	if err != nil {
		return
	}
	value, err := strconv.Atoi(port)
	if err != nil {
		return
	}
	response := &model.Response{
		Reply:    constant.Socks5ReplySuccess,
		BindAddr: model.AddrSpec{IP: net.IPv4zero, Port: value},
	}
	if response.WriteToSocks5(proxyConn) != nil {
		return
	}
	_ = socks5.RunUDPAssociateLoop(udpConn, apicommon.NewPacketOverStreamTunnel(proxyConn), &net.Resolver{})
}

func copyBoth(left, right net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = netCopy(right, left)
	}()
	go func() {
		defer wg.Done()
		_, _ = netCopy(left, right)
	}()
	wg.Wait()
}

func netCopy(dst net.Conn, src net.Conn) (int64, error) {
	buffer := make([]byte, 32*1024)
	var total int64
	for {
		n, err := src.Read(buffer)
		if n > 0 {
			written, writeErr := dst.Write(buffer[:n])
			total += int64(written)
			if writeErr != nil {
				return total, writeErr
			}
		}
		if err != nil {
			return total, err
		}
	}
}
