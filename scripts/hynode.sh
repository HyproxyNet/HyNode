#!/bin/bash
# HyNode management script (hynode-cli)
# Usage: hynode-cli [command] [options]

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[0;33m'
CYAN='\033[0;36m'
BOLD='\033[1m'
PLAIN='\033[0m'

SERVICE_NAME="hynode"
CONFIG_FILE="/etc/hynode/config.yaml"
DATA_DIR="/var/lib/hynode"
BINARY="/usr/local/bin/hynode"
REPO="HyproxyNet/HyNode"

ok()   { echo -e "${GREEN}[✓]${PLAIN} $*"; }
warn() { echo -e "${YELLOW}[!]${PLAIN} $*"; }
err()  { echo -e "${RED}[✗]${PLAIN} $*" >&2; }
info() { echo -e "${CYAN}[i]${PLAIN} $*"; }

check_root() {
    [[ $EUID -ne 0 ]] && { err "请以 root 用户运行"; exit 1; }
}

check_installed() {
    if [[ ! -f "$BINARY" ]] || [[ ! -f "/etc/systemd/system/${SERVICE_NAME}.service" ]]; then
        err "HyNode 未安装，请先运行 install.sh"
        exit 1
    fi
}

check_running() {
    systemctl is-active --quiet "${SERVICE_NAME}" 2>/dev/null
}

# ─── Version helpers ────────────────────────────────────────────────────
get_latest_version() {
    curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" 2>/dev/null | grep '"tag_name"' | grep -oP 'v[0-9.]+'
}

get_current_version() {
    "$BINARY" --version 2>/dev/null | grep -oP 'v[0-9.]+' || echo "unknown"
}

# ─── Status display ─────────────────────────────────────────────────────
show_status() {
    echo ""
    echo -e "  ${CYAN}${BOLD}HyNode 状态${PLAIN}"
    echo "  ─────────────────────────────"
    if [[ ! -f "$BINARY" ]]; then
        echo -e "  安装状态: ${RED}未安装${PLAIN}"
        return
    fi
    echo -e "  安装状态: ${GREEN}已安装${PLAIN}"
    local ver
    ver=$(get_current_version)
    echo -e "  当前版本: ${ver}"
    # Check for updates
    local latest
    latest=$(get_latest_version 2>/dev/null)
    if [[ -n "$latest" ]] && [[ "$ver" != "$latest" ]]; then
        echo -e "  最新版本: ${YELLOW}${latest}${PLAIN} (可更新)"
    fi
    if check_running; then
        echo -e "  运行状态: ${GREEN}运行中${PLAIN}"
        # Uptime
        local uptime_str
        uptime_str=$(systemctl show "${SERVICE_NAME}" --property=ActiveEnterTimestamp 2>/dev/null | cut -d= -f2)
        if [[ -n "$uptime_str" ]]; then
            echo -e "  启动时间: ${uptime_str}"
        fi
    else
        echo -e "  运行状态: ${RED}未运行${PLAIN}"
    fi
    echo -e "  开机自启: $(systemctl is-enabled "${SERVICE_NAME}" 2>/dev/null | grep -q enabled && echo -e "${GREEN}已启用${PLAIN}" || echo -e "${YELLOW}未启用${PLAIN}")"
    if [[ -f "$CONFIG_FILE" ]]; then
        local url
        url=$(grep -oP 'url:\s*"\K[^"]+' "$CONFIG_FILE" 2>/dev/null || echo "未配置")
        echo -e "  面板地址: ${url}"
        local nodes
        nodes=$(grep -c '^\s*- id:' "$CONFIG_FILE" 2>/dev/null || echo "0")
        echo -e "  节点数量: ${nodes}"
        local log_level
        log_level=$(grep -oP 'log_level:\s*"\K[^"]+' "$CONFIG_FILE" 2>/dev/null || echo "info")
        echo -e "  日志级别: ${log_level}"
    else
        echo -e "  配置文件: ${RED}不存在${PLAIN}"
    fi
    # BBR status
    local cc
    cc=$(sysctl -n net.ipv4.tcp_congestion_control 2>/dev/null || echo "unknown")
    echo -e "  拥塞控制: ${cc}"
    # FD limits
    local fd_limit
    fd_limit=$(cat /proc/$(systemctl show "${SERVICE_NAME}" --property=MainPID 2>/dev/null | cut -d= -f2)/limits 2>/dev/null | grep "Max open files" | awk '{print $4}' || echo "N/A")
    echo -e "  FD 限制:  ${fd_limit}"
    echo ""
}

# ─── Service management ─────────────────────────────────────────────────
do_start() {
    check_installed
    if check_running; then
        warn "HyNode 已在运行"
        return
    fi
    systemctl start "${SERVICE_NAME}"
    sleep 1
    if check_running; then
        ok "HyNode 已启动"
    else
        err "启动失败，查看日志: hynode-cli log"
    fi
}

do_stop() {
    check_installed
    if ! check_running; then
        warn "HyNode 未在运行"
        return
    fi
    info "正在优雅关闭 (等待最终流量报告)..."
    systemctl stop "${SERVICE_NAME}"
    ok "HyNode 已停止"
}

do_restart() {
    check_installed
    info "正在重启..."
    systemctl restart "${SERVICE_NAME}"
    sleep 1
    if check_running; then
        ok "HyNode 已重启"
    else
        err "重启失败，查看日志: hynode-cli log"
    fi
}

do_status() {
    check_installed
    systemctl status "${SERVICE_NAME}" --no-pager -l
}

do_log() {
    check_installed
    local lines="${1:-100}"
    if [[ "$2" == "-f" ]]; then
        journalctl -u "${SERVICE_NAME}" -e --no-pager -f
    else
        journalctl -u "${SERVICE_NAME}" --no-pager -n "$lines"
    fi
}

do_enable() {
    check_installed
    systemctl enable "${SERVICE_NAME}" >/dev/null 2>&1
    ok "已启用开机自启"
}

do_disable() {
    check_installed
    systemctl disable "${SERVICE_NAME}" >/dev/null 2>&1
    ok "已禁用开机自启"
}

# ─── Configuration ──────────────────────────────────────────────────────
do_config() {
    check_root
    ${EDITOR:-vi} "${CONFIG_FILE}"
    if check_running; then
        info "检测到配置变更，重启 HyNode..."
        do_restart
    fi
}

do_generate() {
    check_root
    echo ""
    info "===== 生成 HyNode 配置 ====="
    echo ""

    read -rp "面板地址 (如 https://panel.example.com): " panel_url
    while [[ -z "$panel_url" ]]; do
        err "面板地址不能为空"
        read -rp "面板地址: " panel_url
    done

    read -rp "面板 Token (server_token): " panel_token
    while [[ -z "$panel_token" ]]; do
        err "Token 不能为空"
        read -rp "面板 Token: " panel_token
    done

    local node_ids=()
    echo ""
    info "添加节点（输入节点 code，输入空行结束）："
    while true; do
        read -rp "  节点 code: " code
        [[ -z "$code" ]] && break
        node_ids+=("$code")
        ok "  已添加: ${code}"
    done

    if [[ ${#node_ids[@]} -eq 0 ]]; then
        err "至少需要一个节点"
        return 1
    fi

    # Optional: log level
    echo ""
    read -rp "日志级别 [info/debug] (默认 info): " log_level
    log_level="${log_level:-info}"

    # Optional: health listen
    read -rp "健康检查地址 (默认 127.0.0.1:9090): " health_listen
    health_listen="${health_listen:-127.0.0.1:9090}"

    local nodes_yaml=""
    for id in "${node_ids[@]}"; do
        nodes_yaml+="  - id: \"${id}\"
    enabled: true
"
    done

    # Backup existing config
    [[ -f "${CONFIG_FILE}" ]] && cp "${CONFIG_FILE}" "${CONFIG_FILE}.bak.$(date +%Y%m%d%H%M%S)"

    mkdir -p "$(dirname "${CONFIG_FILE}")" "${DATA_DIR}"
    cat > "${CONFIG_FILE}" << EOF
panel:
  url: "${panel_url}"
  token: "${panel_token}"

nodes:
${nodes_yaml}
runtime:
  data_dir: "${DATA_DIR}"
  log_level: "${log_level}"
  health_listen: "${health_listen}"

sync: {}

certificate_fallback:
  cert_mode: "none"
EOF

    ok "配置已写入 ${CONFIG_FILE}"

    if check_running; then
        do_restart
    fi
}

# ─── Node management ────────────────────────────────────────────────────
do_add_node() {
    check_root
    [[ ! -f "${CONFIG_FILE}" ]] && { err "配置文件不存在，请先生成配置"; return 1; }

    local code="$1"
    if [[ -z "$code" ]]; then
        read -rp "节点 code: " code
    fi
    [[ -z "$code" ]] && { err "节点 code 不能为空"; return 1; }

    # Check if node already exists
    if grep -q "id: \"${code}\"" "${CONFIG_FILE}" 2>/dev/null; then
        warn "节点 ${code} 已存在"
        return
    fi

    # Add node before runtime section
    sed -i "/^runtime:/i\\  - id: \"${code}\"\\n    enabled: true" "${CONFIG_FILE}"
    ok "已添加节点: ${code}"

    if check_running; then
        do_restart
    fi
}

do_del_node() {
    check_root
    [[ ! -f "${CONFIG_FILE}" ]] && { err "配置文件不存在"; return 1; }

    echo ""
    info "当前节点："
    grep '^\s*- id:' "${CONFIG_FILE}" | sed 's/.*- id: "\(.*\)".*/  \1/'
    echo ""

    local code="$1"
    if [[ -z "$code" ]]; then
        read -rp "要删除的节点 code: " code
    fi
    [[ -z "$code" ]] && return

    if ! grep -q "id: \"${code}\"" "${CONFIG_FILE}" 2>/dev/null; then
        err "节点 ${code} 不存在"
        return 1
    fi

    # Remove the node entry (id line + enabled line)
    sed -i "/id: \"${code}\"/,/enabled:/d" "${CONFIG_FILE}"
    ok "已删除节点: ${code}"

    if check_running; then
        do_restart
    fi
}

# ─── Update / Upgrade ───────────────────────────────────────────────────
do_update() {
    check_root
    local target_version="$1"

    info "检查更新..."

    local current
    current=$(get_current_version)
    echo -e "  当前版本: ${current}"

    local latest
    if [[ -n "$target_version" ]]; then
        latest="$target_version"
        info "目标版本: ${latest}"
    else
        latest=$(get_latest_version)
        if [[ -z "$latest" ]]; then
            err "获取版本号失败，请检查网络"
            return 1
        fi
        echo -e "  最新版本: ${latest}"
        if [[ "$current" == "$latest" ]]; then
            ok "已是最新版本"
            return
        fi
    fi

    echo ""
    read -rp "确认更新到 ${latest}? [Y/n] " choice
    case "$choice" in
        n|N) info "已取消"; return ;;
    esac

    # Backup current binary (keep last 3 backups)
    if [[ -f "${BINARY}" ]]; then
        [[ -f "${BINARY}.bak.2" ]] && mv "${BINARY}.bak.2" "${BINARY}.bak.3"
        [[ -f "${BINARY}.bak.1" ]] && mv "${BINARY}.bak.1" "${BINARY}.bak.2"
        [[ -f "${BINARY}.bak"   ]] && mv "${BINARY}.bak"   "${BINARY}.bak.1"
        cp "${BINARY}" "${BINARY}.bak"
        ok "已备份旧版本"
    fi

    local arch
    case "$(uname -m)" in
        x86_64|amd64)  arch="amd64" ;;
        aarch64|arm64) arch="arm64" ;;
        armv7l)        arch="arm" ;;
        *)             err "不支持的架构"; return 1 ;;
    esac

    local filename="hynode-linux-${arch}.tar.gz"
    local url="https://github.com/${REPO}/releases/download/${latest}/${filename}"

    local was_running=false
    check_running && was_running=true && systemctl stop "${SERVICE_NAME}"

    info "下载 ${filename}..."
    if ! wget -q --show-progress -O "/tmp/${filename}" "$url"; then
        err "下载失败: ${url}"
        # Restore if was running
        [[ "$was_running" == true ]] && systemctl start "${SERVICE_NAME}"
        return 1
    fi

    # Verify checksum if available
    local checksum_url="${url}.sha256"
    if wget -q -O "/tmp/${filename}.sha256" "$checksum_url" 2>/dev/null; then
        info "校验文件完整性..."
        local expected_hash
        expected_hash=$(awk '{print $1}' "/tmp/${filename}.sha256")
        local actual_hash
        actual_hash=$(sha256sum "/tmp/${filename}" | awk '{print $1}')
        if [[ "$expected_hash" != "$actual_hash" ]]; then
            err "校验失败！文件可能已被篡改"
            rm -f "/tmp/${filename}" "/tmp/${filename}.sha256"
            [[ "$was_running" == true ]] && systemctl start "${SERVICE_NAME}"
            return 1
        fi
        ok "校验通过"
        rm -f "/tmp/${filename}.sha256"
    else
        warn "未找到校验文件，跳过完整性校验"
    fi

    tar -xzf "/tmp/${filename}" -C /tmp/
    mv /tmp/hynode "${BINARY}"
    chmod +x "${BINARY}"
    rm -f "/tmp/${filename}" /tmp/hynode

    ok "已更新到 ${latest}"

    if [[ "$was_running" == true ]]; then
        systemctl start "${SERVICE_NAME}"
        sleep 2
        if check_running; then
            ok "HyNode 已重启"
            echo ""
            show_status
        else
            err "重启失败，正在回滚..."
            do_rollback
        fi
    fi
}

do_rollback() {
    check_root
    if [[ ! -f "${BINARY}.bak" ]]; then
        err "没有可用的备份版本"
        return 1
    fi

    info "回滚到上一个版本..."
    local was_running=false
    check_running && was_running=true && systemctl stop "${SERVICE_NAME}"

    mv "${BINARY}.bak" "${BINARY}"
    chmod +x "${BINARY}"
    # Shift backup chain
    [[ -f "${BINARY}.bak.1" ]] && mv "${BINARY}.bak.1" "${BINARY}.bak"
    [[ -f "${BINARY}.bak.2" ]] && mv "${BINARY}.bak.2" "${BINARY}.bak.1"
    [[ -f "${BINARY}.bak.3" ]] && rm -f "${BINARY}.bak.3"

    if [[ "$was_running" == true ]]; then
        systemctl start "${SERVICE_NAME}"
        sleep 1
        if check_running; then
            ok "已回滚并重启"
        else
            err "回滚后启动失败"
        fi
    else
        ok "已回滚"
    fi
}

# ─── BBR configuration ─────────────────────────────────────────────────
do_bbr() {
    check_root
    show_bbr_menu
}

show_bbr_menu() {
    while true; do
        echo ""
        echo -e "${CYAN}${BOLD}===== BBR 网络优化配置 =====${PLAIN}"
        echo ""
        local current_cc current_qdisc
        current_cc=$(sysctl -n net.ipv4.tcp_congestion_control 2>/dev/null || echo "unknown")
        current_qdisc=$(sysctl -n net.core.default_qdisc 2>/dev/null || echo "unknown")
        echo -e "  当前拥塞控制: ${GREEN}${current_cc}${PLAIN}"
        echo -e "  当前队列调度: ${GREEN}${current_qdisc}${PLAIN}"
        echo ""
        echo "  1.  启用 BBR (仅拥塞控制)"
        echo "  2.  启用 BBR + 全面网络优化 (推荐)"
        echo "  3.  查看当前网络参数"
        echo "  4.  查看优化参数详情"
        echo "  5.  恢复默认网络参数"
        echo "  0.  返回上级菜单"
        echo ""
        read -rp "请选择 [0-5]: " choice
        case "$choice" in
            1)
                sysctl -w net.core.default_qdisc=fq >/dev/null 2>&1
                sysctl -w net.ipv4.tcp_congestion_control=bbr >/dev/null 2>&1
                cat > /etc/sysctl.d/99-hynode-bbr.conf << 'EOF'
net.core.default_qdisc = fq
net.ipv4.tcp_congestion_control = bbr
EOF
                sysctl --system >/dev/null 2>&1
                ok "BBR 已启用"
                ;;
            2)
                configure_bbr_full
                ;;
            3)
                echo ""
                echo -e "${CYAN}── 网络参数概览 ──${PLAIN}"
                printf "  %-30s = %s\n" "tcp_congestion_control" "$(sysctl -n net.ipv4.tcp_congestion_control 2>/dev/null)"
                printf "  %-30s = %s\n" "default_qdisc" "$(sysctl -n net.core.default_qdisc 2>/dev/null)"
                printf "  %-30s = %s\n" "somaxconn" "$(sysctl -n net.core.somaxconn 2>/dev/null)"
                printf "  %-30s = %s\n" "tcp_max_syn_backlog" "$(sysctl -n net.ipv4.tcp_max_syn_backlog 2>/dev/null)"
                printf "  %-30s = %s\n" "tcp_fin_timeout" "$(sysctl -n net.ipv4.tcp_fin_timeout 2>/dev/null)"
                printf "  %-30s = %s\n" "tcp_tw_reuse" "$(sysctl -n net.ipv4.tcp_tw_reuse 2>/dev/null)"
                printf "  %-30s = %s\n" "tcp_fastopen" "$(sysctl -n net.ipv4.tcp_fastopen 2>/dev/null)"
                printf "  %-30s = %s\n" "tcp_keepalive_time" "$(sysctl -n net.ipv4.tcp_keepalive_time 2>/dev/null)"
                printf "  %-30s = %s\n" "ip_local_port_range" "$(sysctl -n net.ipv4.ip_local_port_range 2>/dev/null)"
                printf "  %-30s = %s\n" "rmem_max" "$(sysctl -n net.core.rmem_max 2>/dev/null)"
                printf "  %-30s = %s\n" "wmem_max" "$(sysctl -n net.core.wmem_max 2>/dev/null)"
                printf "  %-30s = %s\n" "tcp_rmem" "$(sysctl -n net.ipv4.tcp_rmem 2>/dev/null)"
                printf "  %-30s = %s\n" "tcp_wmem" "$(sysctl -n net.ipv4.tcp_wmem 2>/dev/null)"
                echo ""
                ;;
            4)
                echo ""
                echo -e "${CYAN}── BBR + 全面网络优化参数详情 ──${PLAIN}"
                echo ""
                echo -e "${BOLD}BBR 拥塞控制${PLAIN}"
                echo "  BBR (Bottleneck Bandwidth and RTT) 是 Google 开发的 TCP 拥塞控制算法，"
                echo "  能自动探测瓶颈带宽和往返延迟，在高延迟、高丢包环境下表现优异。"
                echo ""
                echo -e "${BOLD}连接队列优化${PLAIN}"
                echo "  somaxconn=65535          最大连接队列长度"
                echo "  netdev_max_backlog=65536 网卡接收队列长度"
                echo "  tcp_max_syn_backlog=65536 SYN 队列长度"
                echo ""
                echo -e "${BOLD}连接复用${PLAIN}"
                echo "  tcp_fin_timeout=15       FIN-WAIT-2 超时 (默认60s)"
                echo "  tcp_tw_reuse=1           允许复用 TIME-WAIT 连接"
                echo "  tcp_fastopen=3           TCP Fast Open (客户端+服务端)"
                echo ""
                echo -e "${BOLD}Keepalive 探测${PLAIN}"
                echo "  tcp_keepalive_time=600   空闲 600s 后开始探测"
                echo "  tcp_keepalive_intvl=30   探测间隔 30s"
                echo "  tcp_keepalive_probes=5   最多探测 5 次"
                echo ""
                echo -e "${BOLD}缓冲区${PLAIN}"
                echo "  tcp_rmem/wmem            TCP 读写缓冲区 (4K ~ 16M)"
                echo "  udp_rmem/wmem_min=8192   UDP 最小缓冲区 (QUIC/Hysteria2/TUIC)"
                echo ""
                echo -e "${BOLD}Conntrack (NAT 场景)${PLAIN}"
                echo "  nf_conntrack_max=131072  最大连接跟踪条目"
                echo ""
                ;;
            5)
                rm -f /etc/sysctl.d/99-hynode-bbr.conf
                sysctl -w net.ipv4.tcp_congestion_control=cubic >/dev/null 2>&1
                sysctl -w net.core.default_qdisc=pfifo_fast >/dev/null 2>&1
                sysctl --system >/dev/null 2>&1
                ok "已恢复默认网络参数"
                ;;
            0) return ;;
            *) err "无效选择" ;;
        esac
    done
}

configure_bbr_full() {
    # Check kernel version
    local kernel_major kernel_minor
    kernel_major=$(uname -r | cut -d. -f1)
    kernel_minor=$(uname -r | cut -d. -f2)
    if [[ "$kernel_major" -lt 4 ]] || { [[ "$kernel_major" -eq 4 ]] && [[ "$kernel_minor" -lt 9 ]]; }; then
        err "内核版本 $(uname -r) 过低，BBR 需要 >= 4.9"
        return 1
    fi

    modprobe tcp_bbr 2>/dev/null || true
    sysctl -w net.core.default_qdisc=fq >/dev/null 2>&1
    sysctl -w net.ipv4.tcp_congestion_control=bbr >/dev/null 2>&1

    cat > /etc/sysctl.d/99-hynode-bbr.conf << 'EOF'
# HyNode BBR + network performance optimization
# Optimized for high-concurrency proxy nodes with many users and connections

# BBR congestion control
net.core.default_qdisc = fq
net.ipv4.tcp_congestion_control = bbr

# Connection queue
net.core.somaxconn = 65535
net.core.netdev_max_backlog = 65536
net.ipv4.tcp_max_syn_backlog = 65536

# Connection reuse & timeout
net.ipv4.tcp_fin_timeout = 15
net.ipv4.tcp_tw_reuse = 1
net.ipv4.tcp_fastopen = 3

# Keepalive (detect dead connections faster)
net.ipv4.tcp_keepalive_time = 600
net.ipv4.tcp_keepalive_intvl = 30
net.ipv4.tcp_keepalive_probes = 5

# TCP buffer sizes (high-bandwidth scenarios)
net.ipv4.tcp_rmem = 4096 87380 16777216
net.ipv4.tcp_wmem = 4096 87380 16777216
net.core.rmem_max = 16777216
net.core.wmem_max = 16777216
net.core.rmem_default = 1048576
net.core.wmem_default = 1048576

# UDP buffer sizes (critical for QUIC/Hysteria2/TUIC)
net.ipv4.udp_rmem_min = 8192
net.ipv4.udp_wmem_min = 8192

# Port range
net.ipv4.ip_local_port_range = 1024 65535

# Performance
net.ipv4.tcp_slow_start_after_idle = 0
net.ipv4.tcp_mtu_probing = 1
net.ipv4.tcp_max_tw_buckets = 65536

# Conntrack (for NAT scenarios)
net.netfilter.nf_conntrack_max = 131072
net.netfilter.nf_conntrack_tcp_timeout_established = 7200
net.netfilter.nf_conntrack_tcp_timeout_time_wait = 30
EOF
    sysctl --system >/dev/null 2>&1

    local current_cc
    current_cc=$(sysctl -n net.ipv4.tcp_congestion_control 2>/dev/null)
    if [[ "$current_cc" == "bbr" ]]; then
        ok "BBR + 全面网络优化已启用"
    else
        warn "BBR 设置可能未生效，当前: ${current_cc}"
    fi
}

# ─── Health check ───────────────────────────────────────────────────────
do_health() {
    local port
    port=$(grep -oP 'health_listen:\s*"\K[^"]+' "${CONFIG_FILE}" 2>/dev/null || echo "127.0.0.1:9090")
    local resp
    resp=$(curl -s -w "\n%{http_code}" "http://${port}/healthz" 2>/dev/null)
    local body http_code
    body=$(echo "$resp" | head -1)
    http_code=$(echo "$resp" | tail -1)
    if [[ "$http_code" == "200" ]]; then
        ok "健康检查通过: ${body}"
    else
        err "健康检查失败 (HTTP ${http_code})"
    fi
}

# ─── Uninstall ──────────────────────────────────────────────────────────
do_uninstall() {
    check_root
    echo -e "${RED}警告: 此操作将删除 HyNode 及其所有配置${PLAIN}"
    read -rp "确认卸载? [y/N] " choice
    case "$choice" in
        y|Y) ;;
        *)   info "已取消"; return ;;
    esac

    check_running && systemctl stop "${SERVICE_NAME}"
    systemctl disable "${SERVICE_NAME}" >/dev/null 2>&1
    rm -f "/etc/systemd/system/${SERVICE_NAME}.service"
    systemctl daemon-reload
    rm -f "${BINARY}" "${BINARY}.bak" "${BINARY}.bak."*
    rm -f "/usr/bin/hynode-cli"
    rm -rf "$(dirname "${CONFIG_FILE}")"
    # Keep data dir
    info "数据目录 ${DATA_DIR} 已保留"
    ok "HyNode 已卸载"
}

# ─── Help ───────────────────────────────────────────────────────────────
show_help() {
    echo -e "${CYAN}${BOLD}HyNode 管理工具${PLAIN}"
    echo ""
    echo "用法: hynode-cli [命令] [参数]"
    echo ""
    echo -e "${BOLD}服务管理:${PLAIN}"
    echo "  start              启动 HyNode"
    echo "  stop               停止 HyNode"
    echo "  restart            重启 HyNode"
    echo "  status             查看详细运行状态"
    echo "  log [lines] [-f]   查看日志 (默认100行, -f 实时跟踪)"
    echo "  enable             启用开机自启"
    echo "  disable            禁用开机自启"
    echo "  health             健康检查"
    echo ""
    echo -e "${BOLD}配置管理:${plain}"
    echo "  config             编辑配置文件"
    echo "  generate           交互式生成配置"
    echo "  add [code]         添加节点"
    echo "  del [code]         删除节点"
    echo ""
    echo -e "${BOLD}版本管理:${plain}"
    echo "  update [version]   更新到最新版本 (或指定版本)"
    echo "  upgrade [version]  同 update"
    echo "  rollback           回滚到上一个版本"
    echo "  version            显示当前版本"
    echo ""
    echo -e "${BOLD}系统优化:${plain}"
    echo "  bbr                BBR 网络优化配置菜单"
    echo ""
    echo "  uninstall          卸载 HyNode"
    echo "  help               显示此帮助"
    echo ""
}

do_version() {
    local ver
    ver=$(get_current_version)
    echo -e "HyNode ${GREEN}${ver}${PLAIN}"
}

# ─── Interactive menu ───────────────────────────────────────────────────
show_menu() {
    while true; do
        echo ""
        echo -e "${CYAN}${BOLD}===== HyNode 管理 =====${PLAIN}"
        echo ""
        show_status
        echo "  ── 服务管理 ──────────────"
        echo "  1.   启动"
        echo "  2.   停止"
        echo "  3.   重启"
        echo "  4.   查看状态"
        echo "  5.   查看日志"
        echo ""
        echo "  ── 配置管理 ──────────────"
        echo "  6.   编辑配置"
        echo "  7.   生成配置"
        echo "  8.   添加节点"
        echo "  9.   删除节点"
        echo ""
        echo "  ── 版本管理 ──────────────"
        echo "  10.  更新"
        echo "  11.  回滚"
        echo ""
        echo "  ── 系统优化 ──────────────"
        echo "  12.  BBR 配置"
        echo "  13.  健康检查"
        echo ""
        echo "  0.   退出"
        echo ""
        read -rp "请选择 [0-13]: " choice
        case "$choice" in
            1)  do_start ;;
            2)  do_stop ;;
            3)  do_restart ;;
            4)  do_status ;;
            5)  do_log ;;
            6)  do_config ;;
            7)  do_generate ;;
            8)  do_add_node ;;
            9)  do_del_node ;;
            10) do_update ;;
            11) do_rollback ;;
            12) do_bbr ;;
            13) do_health ;;
            0)  exit 0 ;;
            *)  err "无效选择" ;;
        esac
        echo ""
        read -rp "按回车返回菜单..." _
    done
}

# ─── CLI dispatch ───────────────────────────────────────────────────────
case "${1:-}" in
    start)     do_start ;;
    stop)      do_stop ;;
    restart)   do_restart ;;
    status)    show_status ;;
    log)       do_log "${2:-100}" "${3:-}" ;;
    enable)    do_enable ;;
    disable)   do_disable ;;
    config)    do_config ;;
    generate)  do_generate ;;
    add)       do_add_node "$2" ;;
    del)       do_del_node "$2" ;;
    update)    do_update "$2" ;;
    upgrade)   do_update "$2" ;;
    rollback)  do_rollback ;;
    version)   do_version ;;
    bbr)       do_bbr ;;
    uninstall) do_uninstall ;;
    health)    do_health ;;
    help|-h|--help) show_help ;;
    "")        show_menu ;;
    *)         err "未知命令: $1"; show_help; exit 1 ;;
esac
