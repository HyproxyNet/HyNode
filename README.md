# HyNode

HyNode 是 [HyBoard](https://github.com/HyproxyNet/HyBoard) 的高性能 Go 节点后端。它对接 HyBoard v2 REST API，嵌入 [sing-box](https://github.com/SagerNet/sing-box) 与 [mieru](https://github.com/enfein/mieru) 作为协议内核。

## 功能特性

- 基于 `/api/v2/server/config`、`/user`、`/report` 的 REST 控制循环，支持 ETag 轮询。
- 单进程承载多个 HyBoard 节点，共享设备限制状态。
- 按节点上报流量、在线 IP、连接数、系统指标。
- 进程级设备/IP 限制与按用户带宽限速。
- 嵌入式 sing-box 支持：Shadowsocks、VMess、VLESS、Trojan、Hysteria v1/v2、TUIC、AnyTLS、SOCKS、Naive、HTTP。
- Mieru TCP/UDP 适配器，使用官方嵌入式 API。
- 证书模式：self-signed、http-01、dns-01（Cloudflare、AliDNS）、content、file。
- 面板下发的 multiplex、transport、routes、custom_outbounds 透传至 sing-box。

## 一键安装

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/HyproxyNet/HyNode/main/scripts/install.sh)
```

安装后使用 `hynode-cli` 管理：

```bash
hynode-cli          # 交互菜单
hynode-cli status   # 查看状态
hynode-cli log      # 实时日志
hynode-cli restart  # 重启
hynode-cli add      # 添加节点
hynode-cli update   # 更新版本
```

## 手动安装

### 从 Release 下载

从 [Releases](https://github.com/HyproxyNet/HyNode/releases) 下载对应架构的二进制，解压到 `/usr/local/bin/hynode`。

### 从源码编译

```bash
git clone https://github.com/HyproxyNet/HyNode.git
cd HyNode
make build
```

### 配置

```bash
mkdir -p /etc/hynode /var/lib/hynode
cat > /etc/hynode/config.yaml << 'EOF'
panel:
  url: "https://你的面板地址"
  token: "面板的server_token"
nodes:
  - id: "节点的code"
    enabled: true
runtime:
  data_dir: "/var/lib/hynode"
  log_level: "info"
EOF
```

### systemd 服务

```bash
cat > /etc/systemd/system/hynode.service << 'EOF'
[Unit]
Description=HyNode - HyBoard node backend
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/hynode run -c /etc/hynode/config.yaml
Restart=on-failure
RestartSec=3
LimitNOFILE=1048576

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable --now hynode
journalctl -u hynode -f
```

### Docker

```bash
docker build -t hynode:latest .
docker run -d --name hynode --restart unless-stopped \
  --network host \
  -v /etc/hynode/config.yaml:/etc/hynode/config.yaml:ro \
  -v hynode-data:/var/lib/hynode \
  hynode:latest
```

## 节点 ID 解析

`nodes[].id` 作为 `node-id` header 发送给面板。面板的解析逻辑：

- **纯字符串**（如 `"hk-trojan"`）→ 按 `code` 字段匹配，无歧义。
- **纯数字**（如 `"1"`）→ 同时匹配自增 `id` 和 `code` 字段，有歧义。

**推荐使用节点的自定义 code。**

## 协议边界

- 面板下发的 `multiplex` 设置会透传给支持该功能的 sing-box 入站协议。
- `shadowsocks` 的 `plugin`/`plugin_opts` 会被拒绝。
- `vless` 的非 `none` `decryption` 会被拒绝。
- `tuic` v4 token 认证会被拒绝。
- Mieru 仅接受 `direct` 和 `block` 路由，拒绝 `proxy` 路由和自定义出站。
- 设备限制在同一 HyNode 进程内跨节点严格执行。
- 配置/用户变更自动重建运行时。关闭时发送最后一次流量报告。

## 环境要求

- Go 1.24.7+（仅源码编译需要）
- Linux amd64/arm64/arm
- 面板的 server_token

## 许可证

GPL-3.0-or-later
