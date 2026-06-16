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
- **分片锁架构**：64 分片用户状态锁，高并发下锁争用降低 ~64 倍。
- **批量流量统计**：共享后台 flusher 替代逐连接 goroutine，大幅减少高并发场景下的 goroutine 开销。
- **用户热重载**：仅用户列表变化时无需重建运行时，避免断流。
- **BBR 拥塞控制**：脚本内置 BBR 一键配置 + 全面网络性能优化参数。
- **断流优化**：热重载时短暂排空旧实例，减少连接重置。
- **上游同步**：同步 sing-box 最新核心修复，包括 UoT 竞态修复、TLS 关闭修复、H3 连接泄漏修复等。

## 一键安装

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/HyproxyNet/HyNode/main/scripts/install.sh)
```

### 非交互式安装（自动化部署）

```bash
bash <(curl -fsSL ...) \
  --url https://panel.example.com \
  --token YOUR_SERVER_TOKEN \
  --nodes node1,node2,node3 \
  --bbr \
  --yes
```

| 参数 | 说明 |
|------|------|
| `--url` | 面板地址 |
| `--token` | 面板 server_token |
| `--nodes` | 逗号分隔的节点 code 列表 |
| `--bbr` | 安装后自动启用 BBR + 网络优化 |
| `--yes` | 跳过确认提示 |
| `--version` | 安装指定版本 (如 `v1.0.0`) |
| `--log-level` | 日志级别 (`info` / `debug`) |
| `--data-dir` | 数据目录 (默认 `/var/lib/hynode`) |
| `--listen` | 健康检查监听地址 (默认 `127.0.0.1:9090`) |

## 管理脚本 (hynode-cli)

安装后使用 `hynode-cli` 管理节点：

### 服务管理

```bash
hynode-cli              # 交互菜单
hynode-cli start        # 启动
hynode-cli stop         # 停止 (优雅关闭，发送最终流量报告)
hynode-cli restart      # 重启
hynode-cli status       # 查看状态 (含版本、更新提示、FD限制)
hynode-cli log          # 查看最近 100 行日志
hynode-cli log 500 -f   # 查看最近 500 行并实时跟踪
hynode-cli health       # 健康检查
hynode-cli version      # 显示当前版本
```

### 配置管理

```bash
hynode-cli config       # 编辑配置文件
hynode-cli generate     # 交互式生成配置 (含日志级别、监听地址配置)
hynode-cli add node1    # 添加节点
hynode-cli del node1    # 删除节点
```

### 版本管理

```bash
hynode-cli update           # 更新到最新版本
hynode-cli update v1.2.0    # 更新到指定版本
hynode-cli upgrade          # 同 update
hynode-cli rollback         # 回滚到上一个版本
```

更新失败时自动回滚到旧版本。支持备份链管理（保留最近 3 个版本）。

### BBR 网络优化

```bash
hynode-cli bbr          # 打开 BBR 配置菜单
```

BBR 二级菜单选项：

| 选项 | 说明 |
|------|------|
| 启用 BBR | 仅启用 BBR 拥塞控制 |
| 启用 BBR + 全面网络优化 | BBR + TCP/UDP 参数调优 (推荐) |
| 查看网络参数 | 显示当前所有网络配置 |
| 查看优化参数详情 | 显示各项参数的含义和作用 |
| 恢复默认 | 还原系统默认网络参数 |

网络优化包含：

- **BBR 拥塞控制**：自动探测带宽和延迟，高丢包环境表现优异
- **连接队列**：`somaxconn`、`netdev_max_backlog`、`tcp_max_syn_backlog` = 65535/65536
- **连接复用**：`tcp_fin_timeout=15`、`tcp_tw_reuse=1`、`tcp_fastopen=3`
- **Keepalive**：`tcp_keepalive_time=600`、`intvl=30`、`probes=5`
- **TCP 缓冲区**：`tcp_rmem/wmem` = 4K ~ 16M
- **UDP 缓冲区**：`udp_rmem/wmem_min=8192` (QUIC/Hysteria2/TUIC 关键)
- **Conntrack**：`nf_conntrack_max=131072` (NAT 场景)

## 手动安装

### 从 Release 下载

从 [Releases](https://github.com/HyproxyNet/HyNode/releases) 下载对应架构的二进制，解压到 `/usr/local/bin/hynode`。

### 从源码编译

```bash
git clone https://github.com/HyproxyNet/HyNode.git
cd HyNode
make build          # linux/amd64
make build-all      # amd64 + arm64 + arm
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
  health_listen: "127.0.0.1:9090"
sync: {}
certificate_fallback:
  cert_mode: "none"
EOF
```

#### 环境变量覆盖

| 变量 | 说明 |
|------|------|
| `HYBOARD_URL` | 覆盖 `panel.url` |
| `HYBOARD_TOKEN` | 覆盖 `panel.token` |
| `HYNODE_NODE_ID` | 无 nodes 配置时创建单节点 |

### systemd 服务

```bash
cat > /etc/systemd/system/hynode.service << 'EOF'
[Unit]
Description=HyNode - HyBoard 高性能节点后端
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/hynode run -c /etc/hynode/config.yaml
Restart=on-failure
RestartSec=5
LimitNOFILE=1048576
LimitNPROC=65536
LimitCORE=infinity
TimeoutStopSec=10
KillMode=mixed

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
- 仅配置变更时全量重建运行时；仅用户变更时热重载，避免断流。
- 关闭时发送最后一次流量报告。

## 性能优化

### 高并发架构

- **分片锁**：用户状态按 64 分片独立加锁，多核并发下锁争用极低。
- **共享 flusher**：所有连接共享一个后台 goroutine 定时批量刷入流量统计，替代逐连接 goroutine 模型。万级连接下仅需 1 个 flusher goroutine。
- **连接池优化**：面板 HTTP 客户端启用 HTTP/2、128 连接池、180s 空闲超时、TCP KeepAlive 30s。

### 断流优化

- 用户列表变更时，通过 `HotReloader` 接口重建 sing-box 实例，旧实例短暂排空后关闭，减少连接重置。
- 仅面板配置 (端口/协议/TLS) 变更时才执行全量重启。
- 优雅关闭时发送最终流量报告，确保数据不丢失。

### 上游核心优化

定期同步 sing-box 上游最新修复，近期重要更新：

- **UoT 竞态修复**：修复 UDP-over-TCP 多路复用下的写入和连接竞态条件。
- **TLS 服务端关闭修复**：修复 TLS 连接不正确的关闭方式导致的连接挂起。
- **H3 连接泄漏修复**：修复 HTTP/3 连接在远端取消时未正确标记为已关闭的问题。
- **BBR 拥塞控制修复**：修复 QUIC BBR 窗口缩放和算法逻辑问题。
- **ShadowTLS 握手修复**：修复可能导致连接失败的握手问题。
- **LRU 缓存修复**：修复 DNS 缓存和连接跟踪的 LRU 生命周期刷新问题。

### BBR 网络调优

通过 `hynode-cli bbr` 一键配置：

```
net.ipv4.tcp_congestion_control = bbr
net.core.default_qdisc = fq
net.core.somaxconn = 65535
net.ipv4.tcp_fin_timeout = 15
net.ipv4.tcp_tw_reuse = 1
net.ipv4.tcp_fastopen = 3
net.ipv4.tcp_rmem = 4096 87380 16777216
net.ipv4.tcp_wmem = 4096 87380 16777216
net.ipv4.udp_rmem_min = 8192
net.ipv4.udp_wmem_min = 8192
net.ipv4.ip_local_port_range = 1024 65535
net.netfilter.nf_conntrack_max = 131072
```

## 环境要求

- Go 1.24.7+（仅源码编译需要）
- Linux amd64/arm64/arm
- 内核 >= 4.9（BBR 需要）
- 面板的 server_token

## 许可证

GPL-3.0-or-later
