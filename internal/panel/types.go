package panel

import (
	"encoding/json"
	"strings"
)

type NodeConfig struct {
	Protocol        string          `json:"protocol"`
	ListenIP        string          `json:"listen_ip"`
	ServerPort      int             `json:"server_port"`
	Network         string          `json:"network"`
	NetworkSettings map[string]any  `json:"networkSettings"`
	Cipher          string          `json:"cipher"`
	Plugin          string          `json:"plugin"`
	PluginOpts      string          `json:"plugin_opts"`
	ServerKey       string          `json:"server_key"`
	TLS             int             `json:"tls"`
	AllowInsecure   bool            `json:"allow_insecure"`
	Multiplex       map[string]any  `json:"multiplex"`
	Host            string          `json:"host"`
	ServerName      string          `json:"server_name"`
	Flow            string          `json:"flow"`
	Decryption      string          `json:"decryption"`
	TLSSettings     map[string]any  `json:"tls_settings"`
	Version         int             `json:"version"`
	UpMbps          int             `json:"up_mbps"`
	DownMbps        int             `json:"down_mbps"`
	Obfs            any             `json:"obfs"`
	ObfsPassword    string          `json:"obfs-password"`
	Congestion      string          `json:"congestion_control"`
	AuthTimeout     string          `json:"auth_timeout"`
	ZeroRTT         bool            `json:"zero_rtt_handshake"`
	Heartbeat       string          `json:"heartbeat"`
	PaddingScheme   any             `json:"padding_scheme"`
	Transport       string          `json:"transport"`
	TrafficPattern  string          `json:"traffic_pattern"`
	Routes          []Route         `json:"routes"`
	CustomOutbounds json.RawMessage `json:"custom_outbounds"`
	CustomRoutes    json.RawMessage `json:"custom_routes"`
	CertConfig      map[string]any  `json:"cert_config"`
	BaseConfig      BaseConfig      `json:"base_config"`
}

type BaseConfig struct {
	PushInterval int `json:"push_interval"`
	PullInterval int `json:"pull_interval"`
}

type Route struct {
	ID          int64    `json:"id"`
	Match       []string `json:"match"`
	Action      string   `json:"action"`
	ActionValue string   `json:"action_value"`
}

type User struct {
	ID          int64  `json:"id"`
	UUID        string `json:"uuid"`
	SpeedLimit  int    `json:"speed_limit"`
	DeviceLimit int    `json:"device_limit"`
}

type UsersResponse struct {
	Users []User `json:"users"`
}

type Report struct {
	Traffic map[string][2]int64 `json:"traffic,omitempty"`
	Alive   map[string][]string `json:"alive,omitempty"`
	Online  map[string]int      `json:"online,omitempty"`
	Status  map[string]any      `json:"status,omitempty"`
	Metrics map[string]any      `json:"metrics,omitempty"`
}

// NodeType returns the node-type header value for the HyBoard ServerGuard.
// The guard normalizes certain protocol names (e.g. v2ray -> vmess, hysteria2 -> hysteria).
func (c NodeConfig) NodeType() string {
	p := strings.ToLower(c.Protocol)
	switch p {
	case "hysteria":
		if c.Version == 2 {
			return "hysteria2"
		}
		return "hysteria"
	default:
		return p
	}
}
