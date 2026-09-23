#!/bin/bash
# HyNode one-click installation script
# Usage:
#   Interactive:   bash <(curl -fsSL https://raw.githubusercontent.com/HyproxyNet/HyNode/main/scripts/install.sh)
#   Non-interactive: bash <(curl -fsSL ...) --url https://panel.example.com --token YOUR_TOKEN --nodes node1,node2 [--bbr] [--yes]

set -euo pipefail

red='\033[0;31m'
green='\033[0;32m'
yellow='\033[0;33m'
cyan='\033[0;36m'
bold='\033[1m'
plain='\033[0m'

REPO="HyproxyNet/HyNode"
INSTALL_DIR="/usr/local/bin"
CONFIG_DIR="/etc/hynode"
DATA_DIR="/var/lib/hynode"
SERVICE_NAME="hynode"
BINARY="${INSTALL_DIR}/hynode"
CLI_URL="https://raw.githubusercontent.com/${REPO}/main/scripts/hynode.sh"

ok()   { echo -e "${green}[✓]${plain} $*"; }
warn() { echo -e "${yellow}[!]${plain} $*"; }
err()  { echo -e "${red}[✗]${plain} $*" >&2; }
info() { echo -e "${cyan}[i]${plain} $*"; }

# ─── CLI argument parsing ───────────────────────────────────────────────
ARG_URL=""
ARG_TOKEN=""
ARG_NODES=""
ARG_BBR=false
ARG_SKIP_CONFIRM=false
ARG_VERSION=""
ARG_LOG_LEVEL="info"
ARG_DATA_DIR=""
ARG_LISTEN=""

parse_args() {
    while [[ $# -gt 0 ]]; do
        case "$1" in
            --url)          ARG_URL="$2";          shift 2 ;;
            --token)        ARG_TOKEN="$2";         shift 2 ;;
            --nodes)        ARG_NODES="$2";         shift 2 ;;
            --bbr)          ARG_BBR=true;           shift ;;
            --yes|-y)       ARG_SKIP_CONFIRM=true;  shift ;;
            --version)      ARG_VERSION="$2";       shift 2 ;;
            --log-level)    ARG_LOG_LEVEL="$2";     shift 2 ;;
            --data-dir)     ARG_DATA_DIR="$2";      shift 2 ;;
            --listen)       ARG_LISTEN="$2";        shift 2 ;;
            -h|--help)
                echo -e "${bold}HyNode Installer${plain} - HyBoard 高性能节点后端"
                echo ""
                echo "用法: install.sh [选项]"
                echo ""
                echo -e "${bold}核心选项:${plain}"
                echo "  --url URL           面板地址 (如 https://panel.example.com)"
                echo "  --token TOKEN       面板 server_token"
                echo "  --nodes IDS         逗号分隔的节点 code (如 node1,node2,node3)"
                echo ""
                echo -e "${bold}可选配置:${plain}"
                echo "  --version VER       安装指定版本 (如 v1.0.0，默认最新)"
                echo "  --log-level LEVEL   日志级别 (info|debug, 默认 info)"
                echo "  --data-dir DIR      数据目录 (默认 /var/lib/hynode)"
                echo "  --listen ADDR       健康检查监听地址 (默认 127.0.0.1:9090)"
                echo "  --bbr               安装后自动启用 BBR + 网络优化"
                echo "  --yes, -y           跳过所有确认提示"
                echo "  -h, --help          显示帮助信息"
                echo ""
                echo -e "${bold}示例:${plain}"
                echo "  # 交互式安装"
                echo "  bash <(curl -fsSL https://raw.githubusercontent.com/${REPO}/main/scripts/install.sh)"
                echo ""
                echo "  # 一键自动部署"
                echo "  bash <(curl -fsSL ...) --url https://panel.example.com \\"
                echo "    --token YOUR_TOKEN --nodes node1,node2 --bbr --yes"
                exit 0
                ;;
            *) err "未知选项: $1 (使用 --help 查看帮助)"; exit 1 ;;
        esac
    done
}

# ─── Pre-flight checks ──────────────────────────────────────────────────
check_root() {
    if [[ $EUID -ne 0 ]]; then
        err "请以 root 用户运行此脚本 (sudo bash install.sh)"
        exit 1
    fi
}

detect_arch() {
    ARCH=$(uname -m)
    case "$ARCH" in
        x86_64|amd64)  ARCH="amd64" ;;
        aarch64|arm64) ARCH="arm64" ;;
        armv7l)        ARCH="arm" ;;
        *)             err "不支持的架构: $ARCH"; exit 1 ;;
    esac
    ok "检测架构: linux/${ARCH}"
}

detect_os() {
    if [[ -f /etc/redhat-release ]]; then
        PKG_MGR="yum"
    elif [[ -f /etc/debian_version ]] || grep -qi ubuntu /etc/issue 2>/dev/null; then
        PKG_MGR="apt"
    else
        warn "未识别的包管理器，尝试继续..."
        PKG_MGR="unknown"
    fi
    ok "包管理器: ${PKG_MGR}"
}

install_deps() {
    info "安装系统依赖..."
    if [[ "$PKG_MGR" == "apt" ]]; then
        apt-get update -qq
        apt-get install -y -qq wget curl ca-certificates 2>/dev/null
    elif [[ "$PKG_MGR" == "yum" ]]; then
        yum install -y -q wget curl ca-certificates 2>/dev/null
    else
        warn "跳过依赖安装 (未知包管理器)"
    fi
    ok "系统依赖已就绪"
}

# ─── Version management ─────────────────────────────────────────────────
get_latest_version() {
    local ver
    ver=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" 2>/dev/null | grep '"tag_name"' | grep -oP 'v[0-9.]+')
    echo "$ver"
}

get_current_version() {
    if [[ -f "$BINARY" ]]; then
        "$BINARY" --version 2>/dev/null | grep -oP 'v[0-9.]+' || echo "unknown"
    else
        echo "not_installed"
    fi
}

# ─── Binary installation ────────────────────────────────────────────────
install_binary() {
    local version="$1"
    if [[ -z "$version" ]]; then
        info "获取最新版本..."
        version=$(get_latest_version)
    fi
    if [[ -z "$version" ]]; then
        err "获取版本号失败"
        err "请先在 GitHub 上创建 Release，或手动安装二进制到 ${BINARY}"
        exit 1
    fi
    ok "目标版本: ${version}"

    local filename="hynode-linux-${ARCH}.tar.gz"
    local url="https://github.com/${REPO}/releases/download/${version}/${filename}"

    info "下载 ${filename}..."
    if ! wget -q --show-progress -O "/tmp/${filename}" "$url"; then
        err "下载失败: ${url}"
        exit 1
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
            err "  期望: ${expected_hash}"
            err "  实际: ${actual_hash}"
            rm -f "/tmp/${filename}" "/tmp/${filename}.sha256"
            exit 1
        fi
        ok "校验通过 (SHA256: ${actual_hash:0:16}...)"
        rm -f "/tmp/${filename}.sha256"
    else
        warn "未找到校验文件，跳过完整性校验"
    fi

    info "解压安装..."
    # Backup existing binary before upgrade (keep last 3 backups)
    if [[ -f "${BINARY}" ]]; then
        # Rotate backups
        [[ -f "${BINARY}.bak.2" ]] && mv "${BINARY}.bak.2" "${BINARY}.bak.3"
        [[ -f "${BINARY}.bak.1" ]] && mv "${BINARY}.bak.1" "${BINARY}.bak.2"
        [[ -f "${BINARY}.bak"   ]] && mv "${BINARY}.bak"   "${BINARY}.bak.1"
        cp "${BINARY}" "${BINARY}.bak"
        ok "已备份旧版本"
    fi

    tar -xzf "/tmp/${filename}" -C /tmp/
    mv /tmp/hynode "${BINARY}"
    chmod +x "${BINARY}"
    rm -f "/tmp/${filename}" /tmp/hynode

    ok "二进制已安装到 ${BINARY}"
}

# ─── Systemd service ────────────────────────────────────────────────────
install_service() {
    info "安装 systemd 服务..."
    cat > "/etc/systemd/system/${SERVICE_NAME}.service" << 'EOF'
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

# Performance tuning for high-concurrency multi-user scenarios
LimitNPROC=65536
LimitCORE=infinity

# Graceful shutdown: send SIGTERM, wait 10s for final report
TimeoutStopSec=10
KillMode=mixed

[Install]
WantedBy=multi-user.target
EOF
    systemctl daemon-reload
    systemctl enable "${SERVICE_NAME}" 2>/dev/null
    ok "systemd 服务已安装"
}

# ─── Configuration generation ───────────────────────────────────────────
generate_config() {
    local panel_url="$ARG_URL"
    local panel_token="$ARG_TOKEN"
    local node_ids_str="$ARG_NODES"

    echo ""
    info "===== 配置 HyNode ====="
    echo ""

    # Use CLI args if provided, otherwise prompt interactively
    if [[ -z "$panel_url" ]]; then
        read -rp "面板地址 (如 https://panel.example.com): " panel_url
        while [[ -z "$panel_url" ]]; do
            err "面板地址不能为空"
            read -rp "面板地址: " panel_url
        done
    fi

    if [[ -z "$panel_token" ]]; then
        read -rp "面板 Token (server_token): " panel_token
        while [[ -z "$panel_token" ]]; do
            err "Token 不能为空"
            read -rp "面板 Token: " panel_token
        done
    fi

    local node_ids=()
    if [[ -n "$node_ids_str" ]]; then
        IFS=',' read -ra node_ids <<< "$node_ids_str"
    else
        echo ""
        info "添加节点（输入节点 code，输入空行结束）："
        while true; do
            read -rp "  节点 code: " code
            [[ -z "$code" ]] && break
            node_ids+=("$code")
            ok "  已添加: ${code}"
        done
    fi

    if [[ ${#node_ids[@]} -eq 0 ]]; then
        err "至少需要一个节点"
        return 1
    fi

    local nodes_yaml=""
    for id in "${node_ids[@]}"; do
        id=$(echo "$id" | xargs)  # trim whitespace
        nodes_yaml+="  - id: \"${id}\"
    enabled: true
"
    done

    local listen_addr="${ARG_LISTEN:-127.0.0.1:9090}"
    local data_dir="${ARG_DATA_DIR:-${DATA_DIR}}"

    mkdir -p "${CONFIG_DIR}" "${data_dir}"
    cat > "${CONFIG_DIR}/config.yaml" << EOF
panel:
  url: "${panel_url}"
  token: "${panel_token}"

nodes:
${nodes_yaml}
runtime:
  data_dir: "${data_dir}"
  log_level: "${ARG_LOG_LEVEL}"
  health_listen: "${listen_addr}"

sync: {}

certificate_fallback:
  cert_mode: "none"
EOF
    ok "配置已写入 ${CONFIG_DIR}/config.yaml"
}

# ─── BBR configuration ─────────────────────────────────────────────────
configure_bbr() {
    info "配置 BBR 拥塞控制..."

    # Check kernel version (>= 4.9 required for BBR)
    local kernel_major kernel_minor
    kernel_major=$(uname -r | cut -d. -f1)
    kernel_minor=$(uname -r | cut -d. -f2)
    if [[ "$kernel_major" -lt 4 ]] || { [[ "$kernel_major" -eq 4 ]] && [[ "$kernel_minor" -lt 9 ]]; }; then
        err "内核版本 $(uname -r) 过低，BBR 需要 >= 4.9"
        return 1
    fi

    # Check if BBR module is available
    if ! modprobe tcp_bbr 2>/dev/null && [[ ! -d /proc/sys/net/ipv4/tcp_bbr ]]; then
        warn "tcp_bbr 模块不可用，尝试继续..."
    fi

    # Apply BBR settings
    sysctl -w net.core.default_qdisc=fq >/dev/null 2>&1
    sysctl -w net.ipv4.tcp_congestion_control=bbr >/dev/null 2>&1

    # Persist across reboots
    local sysctl_conf="/etc/sysctl.d/99-hynode-bbr.conf"
    cat > "$sysctl_conf" << 'EOF'
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

    # Verify
    local current_cc
    current_cc=$(sysctl -n net.ipv4.tcp_congestion_control 2>/dev/null)
    if [[ "$current_cc" == "bbr" ]]; then
        ok "BBR 已启用 (congestion_control=${current_cc})"
        ok "网络优化参数已写入 ${sysctl_conf}"
    else
        warn "BBR 设置可能未生效，当前: ${current_cc}"
    fi
}

# ─── CLI management script ──────────────────────────────────────────────
install_cli() {
    info "安装管理脚本..."
    if wget -q -O "/usr/bin/hynode-cli" "$CLI_URL"; then
        chmod +x "/usr/bin/hynode-cli"
        ok "管理脚本已安装: hynode-cli"
    else
        warn "下载管理脚本失败，请手动下载"
    fi
}

# ─── Summary ────────────────────────────────────────────────────────────
show_summary() {
    echo ""
    echo -e "${bold}============================================${plain}"
    ok "安装完成！"
    echo ""
    echo -e "  ${cyan}管理命令:${plain}   hynode-cli"
    echo -e "  ${cyan}配置文件:${plain}   ${CONFIG_DIR}/config.yaml"
    echo -e "  ${cyan}启动服务:${plain}   systemctl start hynode"
    echo -e "  ${cyan}查看日志:${plain}   hynode-cli log"
    echo -e "  ${cyan}BBR 配置:${plain}   hynode-cli bbr"
    echo -e "  ${cyan}健康检查:${plain}   hynode-cli health"
    echo ""
    echo -e "  ${yellow}常用命令:${plain}"
    echo "    hynode-cli status    # 查看运行状态"
    echo "    hynode-cli update    # 更新到最新版本"
    echo "    hynode-cli log -f    # 实时查看日志"
    echo ""
}

restart_after_update() {
    info "检测到 HyNode 已安装且正在运行，正在重启服务以加载新版本..."
    if systemctl restart "${SERVICE_NAME}"; then
        sleep 2
        if systemctl is-active --quiet "${SERVICE_NAME}"; then
            ok "HyNode 已更新并重启"
            return 0
        fi
    fi

    err "新版本启动失败"
    if [[ -f "${BINARY}.bak" ]]; then
        warn "正在恢复旧版本 binary 并重启..."
        cp "${BINARY}.bak" "${BINARY}"
        chmod +x "${BINARY}"
        if systemctl restart "${SERVICE_NAME}"; then
            sleep 2
            if systemctl is-active --quiet "${SERVICE_NAME}"; then
                ok "已回滚到旧版本并恢复运行"
                return 0
            fi
        fi
    fi

    warn "请查看日志: journalctl -u hynode -e"
    return 1
}

# ─── Main ───────────────────────────────────────────────────────────────
main() {
    parse_args "$@"

    echo ""
    echo -e "${bold}${cyan}HyNode Installer${plain} - HyBoard 高性能节点后端"
    echo "============================================"
    echo ""

    check_root
    detect_arch
    detect_os

    local was_installed=false
    local was_running=false
    [[ -f "${BINARY}" ]] && was_installed=true
    systemctl is-active --quiet "${SERVICE_NAME}" 2>/dev/null && was_running=true

    install_deps
    install_binary "$ARG_VERSION"
    install_service

    local first_install=false
    if [[ ! -f "${CONFIG_DIR}/config.yaml" ]]; then
        first_install=true
        generate_config || warn "配置生成失败，请稍后运行 hynode-cli generate"
    else
        ok "配置文件已存在，跳过生成"
    fi

    # BBR setup
    if [[ "$ARG_BBR" == true ]]; then
        configure_bbr
    fi

    install_cli
    show_summary

    if [[ "$first_install" == true ]] && [[ -f "${CONFIG_DIR}/config.yaml" ]]; then
        if [[ "$ARG_SKIP_CONFIRM" == true ]]; then
            systemctl start "${SERVICE_NAME}"
            sleep 2
            if systemctl is-active --quiet "${SERVICE_NAME}"; then
                ok "HyNode 已启动"
            else
                warn "启动失败，查看日志: journalctl -u hynode -e"
            fi
        else
            read -rp "是否现在启动? [Y/n] " choice
            case "$choice" in
                n|N) info "稍后运行: systemctl start hynode" ;;
                *)
                    systemctl start "${SERVICE_NAME}"
                    sleep 2
                    if systemctl is-active --quiet "${SERVICE_NAME}"; then
                        ok "HyNode 已启动"
                    else
                        warn "启动失败，查看日志: journalctl -u hynode -e"
                    fi
                    ;;
            esac
        fi
    elif [[ "$was_installed" == true ]] && [[ "$was_running" == true ]]; then
        restart_after_update
    elif [[ "$was_installed" == true ]]; then
        info "HyNode 已更新；服务原本未运行，未自动启动"
    fi
}

main "$@"
