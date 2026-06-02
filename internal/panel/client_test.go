package panel

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestETagAndHeaders(t *testing.T) {
	var seen bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("token") != "secret" || r.Header.Get("node-id") != "node-a" {
			t.Fatal("missing auth headers")
		}
		if r.Header.Get("node-type") != "socks" {
			t.Fatalf("node-type = %q, want socks", r.Header.Get("node-type"))
		}
		if seen {
			if r.Header.Get("If-None-Match") != `"abc"` {
				t.Fatal("missing etag request")
			}
			w.WriteHeader(http.StatusNotModified)
			return
		}
		seen = true
		w.Header().Set("ETag", `"abc"`)
		_ = json.NewEncoder(w).Encode(NodeConfig{Protocol: "socks", ServerPort: 1000})
	}))
	defer server.Close()
	client := NewClient(server.URL, "secret")
	_, etag, changed, err := client.Config(context.Background(), "node-a", "socks", "")
	if err != nil || !changed || etag != `"abc"` {
		t.Fatalf("first config: %q %v %v", etag, changed, err)
	}
	_, _, changed, err = client.Config(context.Background(), "node-a", "socks", etag)
	if err != nil || changed {
		t.Fatalf("second config: %v %v", changed, err)
	}
}

func TestReportShape(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload Report
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload.Traffic["1"] != [2]int64{3, 4} {
			t.Fatalf("traffic = %#v", payload.Traffic)
		}
		_, _ = w.Write([]byte(`{"data":true}`))
	}))
	defer server.Close()
	err := NewClient(server.URL, "secret").Report(context.Background(), "n", "socks", Report{
		Traffic: map[string][2]int64{"1": {3, 4}},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestNodeTypeHeader(t *testing.T) {
	tests := []struct {
		protocol string
		version  int
		want     string
	}{
		{"vmess", 0, "vmess"},
		{"vless", 0, "vless"},
		{"trojan", 0, "trojan"},
		{"shadowsocks", 0, "shadowsocks"},
		{"hysteria", 1, "hysteria"},
		{"hysteria", 2, "hysteria2"},
		{"mieru", 0, "mieru"},
	}
	for _, tt := range tests {
		cfg := NodeConfig{Protocol: tt.protocol, Version: tt.version}
		got := cfg.NodeType()
		if got != tt.want {
			t.Errorf("NodeConfig{%q, version=%d}.NodeType() = %q, want %q", tt.protocol, tt.version, got, tt.want)
		}
	}
}
