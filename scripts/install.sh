#!/bin/bash
# HyNode one-click installation script
# Usage: bash <(curl -fsSL https://raw.githubusercontent.com/HyproxyNet/HyNode/main/scripts/install.sh)

set -e

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[0;33m'
CYAN='\033[0;36m'
PLAIN='\033[0m'

REPO="HyproxyNet/HyNode"
INSTALL_DIR="/usr/local/bin"
CONFIG_DIR="/etc/hynode"
DATA_DIR="/var/lib/hynode"
SERVICE_NAME="hynode"
BINARY="${INSTALL_DIR}/hynode"

ok()   { echo -e "${GREEN}[OK]${PLAIN} $*"; }
warn() { echo -e "${YELLOW}[WARN]${PLAIN} $*"; }
err()  { echo -e "${RED}[ERROR]${PLAIN} $*"; exit 1; }
info() { echo -e "${CYAN}[INFO]${PLAIN} $*"; }

check_root() {
    [[ $EUID -ne 0 ]] && err "请以 root 用户运行此脚本"
}

detect_arch() {
    case "$(uname -m)" in
        x86_64|amd64)  ARCH="amd64" ;;
        aarch64|arm64) ARCH="arm64" ;;
        armv7l)        ARCH="arm" ;;
        *)             err "不支持的架构: $(uname -m)" ;;
    esac
    ok "架构: linux/${ARCH}"
}

detect_os() {
    if [[ -f /etc/redhat-release ]]; then
        PKG_MGR="yum"
    elif [[ -f /etc/debian_version ]] || grep -qi ubuntu /etc/issue 2>/dev/null; then
        PKG_MGR="apt"
    else
        err "不支持的操作系统"
    fi
}

install_deps() {
    info "安装系统依赖..."
    if [[ "$PKG_MGR" == "apt" ]]; then
        apt-get update -qq
        apt-get install -y -qq wget curl ca-certificates >/dev/null 2>&1
    else
        yum install -y -q wget curl ca-certificates >/dev/null 2>&1
    fi
    ok "系统依赖已安装"
}

get_latest_version() {
    local ver
    ver=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" 2>/dev/null | grep '"tag_name"' | grep -oP 'v[0-9.]+')
    if [[ -z "$ver" ]]; then
        err "获取版本号失败，请检查网络或手动指定版本"
    fi
    echo "$ver"
}

install_binary() {
    local version="${1:-}"
    if [[ -z "$version" ]]; then
        info "获取最新版本..."
        version=$(get_latest_version)
    fi
    ok "版本: ${version}"

    local filename="hynode-linux-${ARCH}.tar.gz"
    local url="https://github.com/${REPO}/releases/download/${version}/${filename}"

    info "下载 ${filename}..."
    wget -q -O "/tmp/${filename}" "$url" || err "下载失败: ${url}"

    info "解压安装..."
    tar -xzf "/tmp/${filename}" -C /tmp/
    mv /tmp/hynode "${BINARY}"
    chmod +x "${BINARY}"
    rm -f "/tmp/${filename}" /tmp/hynode

    ok "二进制已安装到 ${BINARY}"
}

install_service() {
    info "安装 systemd 服务..."
    cat > "/etc/systemd/system/${SERVICE_NAME}.service" << 'EOF'
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
    systemctl enable "${SERVICE_NAME}" >/dev/null 2>&1
    ok "systemd 服务已安装"
}

generate_config() {
    echo ""
    info "===== 配置 HyNode ====="
    echo ""

    read -rp "面板地址 (如 https://panel.example.com): " panel_url
    while [[ -z "$panel_url" ]]; do
        err "面板地址不能为空" ; read -rp "面板地址: " panel_url
    done

    read -rp "面板 Token (server_token): " panel_token
    while [[ -z "$panel_token" ]]; do
        err "Token 不能为空" ; read -rp "面板 Token: " panel_token
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

    [[ ${#node_ids[@]} -eq 0 ]] && err "至少需要一个节点"

    local nodes_yaml=""
    for id in "${node_ids[@]}"; do
        nodes_yaml+="  - id: \"${id}\"
    enabled: true
"
    done

    mkdir -p "${CONFIG_DIR}" "${DATA_DIR}"
    cat > "${CONFIG_DIR}/config.yaml" << EOF
panel:
  url: "${panel_url}"
  token: "${panel_token}"

nodes:
${nodes_yaml}
runtime:
  data_dir: "${DATA_DIR}"
  log_level: "info"
  health_listen: "127.0.0.1:9090"

sync: {}

certificate_fallback:
  cert_mode: "none"
EOF
    ok "配置已写入 ${CONFIG_DIR}/config.yaml"
}

install_cli() {
    info "安装管理脚本..."
    wget -q -O "/usr/bin/hynode-cli" \
        "https://raw.githubusercontent.com/${REPO}/main/scripts/hynode.sh" || \
        { warn "下载管理脚本失败，请手动下载"; return; }
    chmod +x "/usr/bin/hynode-cli"
    ok "管理脚本已安装: hynode-cli"
}

main() {
    echo ""
    echo -e "${CYAN}HyNode Installer${PLAIN} - HyBoard 节点后端"
    echo "============================================"
    echo ""

    check_root
    detect_arch
    detect_os
    install_deps
    install_binary "$1"
    install_service

    local first_install=false
    if [[ ! -f "${CONFIG_DIR}/config.yaml" ]]; then
        first_install=true
        generate_config || warn "配置生成失败，请稍后运行 hynode-cli generate"
    fi

    install_cli

    echo ""
    echo "============================================"
    ok "安装完成！"
    echo ""
    echo "  管理命令:   hynode-cli"
    echo "  配置文件:   ${CONFIG_DIR}/config.yaml"
    echo "  启动服务:   systemctl start hynode"
    echo "  查看日志:   hynode-cli log"
    echo ""

    if [[ "$first_install" == true ]] && [[ -f "${CONFIG_DIR}/config.yaml" ]]; then
        read -rp "是否现在启动? [Y/n] " choice
        case "$choice" in
            n|N) info "稍后运行: systemctl start hynode" ;;
            *)   systemctl start "${SERVICE_NAME}"
                 sleep 1
                 systemctl status "${SERVICE_NAME}" --no-pager -l 2>/dev/null || true
                 ;;
        esac
    fi
}

main "$@"
