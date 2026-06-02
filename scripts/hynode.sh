#!/bin/bash
# HyNode management script
# Usage: hynode-cli [command]

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[0;33m'
CYAN='\033[0;36m'
PLAIN='\033[0m'

SERVICE_NAME="hynode"
CONFIG_FILE="/etc/hynode/config.yaml"
DATA_DIR="/var/lib/hynode"
BINARY="/usr/local/bin/hynode"
REPO="HyproxyNet/HyNode"

ok()   { echo -e "${GREEN}[OK]${PLAIN} $*"; }
warn() { echo -e "${YELLOW}[WARN]${PLAIN} $*"; }
err()  { echo -e "${RED}[ERROR]${PLAIN} $*"; }
info() { echo -e "${CYAN}[INFO]${PLAIN} $*"; }

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

show_status() {
    echo ""
    echo -e "  ${CYAN}HyNode 状态${PLAIN}"
    echo "  ─────────────────────────────"
    if [[ ! -f "$BINARY" ]]; then
        echo -e "  安装状态: ${RED}未安装${PLAIN}"
        return
    fi
    echo -e "  安装状态: ${GREEN}已安装${PLAIN}"
    local ver
    ver=$("$BINARY" run -c /dev/null 2>&1 | head -1 || echo "unknown")
    if check_running; then
        echo -e "  运行状态: ${GREEN}运行中${PLAIN}"
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
    else
        echo -e "  配置文件: ${RED}不存在${PLAIN}"
    fi
    echo ""
}

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
    systemctl stop "${SERVICE_NAME}"
    ok "HyNode 已停止"
}

do_restart() {
    check_installed
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
    journalctl -u "${SERVICE_NAME}" -e --no-pager -f
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

    local nodes_yaml=""
    for id in "${node_ids[@]}"; do
        nodes_yaml+="  - id: \"${id}\"
    enabled: true
"
    done

    # Backup existing config
    [[ -f "${CONFIG_FILE}" ]] && cp "${CONFIG_FILE}" "${CONFIG_FILE}.bak"

    mkdir -p "$(dirname "${CONFIG_FILE}")" "${DATA_DIR}"
    cat > "${CONFIG_FILE}" << EOF
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

    ok "配置已写入 ${CONFIG_FILE}"

    if check_running; then
        do_restart
    fi
}

do_add_node() {
    check_root
    [[ ! -f "${CONFIG_FILE}" ]] && { err "配置文件不存在，请先生成配置"; return 1; }

    read -rp "节点 code: " code
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

    read -rp "要删除的节点 code: " code
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
    rm -f "${BINARY}"
    rm -f "/usr/bin/hynode-cli"
    rm -rf "${CONFIG_DIR}"
    # Keep data dir
    info "数据目录 ${DATA_DIR} 已保留"
    ok "HyNode 已卸载"
}

do_update() {
    check_root
    info "更新 HyNode..."

    local version
    version=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" 2>/dev/null | grep '"tag_name"' | grep -oP 'v[0-9.]+')
    if [[ -z "$version" ]]; then
        err "获取版本号失败"
        return 1
    fi

    local current
    current=$("${BINARY}" --version 2>/dev/null | grep -oP 'v[0-9.]+' || echo "unknown")
    info "当前版本: ${current}"
    info "最新版本: ${version}"

    if [[ "$current" == "$version" ]]; then
        ok "已是最新版本"
        return
    fi

    local arch
    case "$(uname -m)" in
        x86_64|amd64)  arch="amd64" ;;
        aarch64|arm64) arch="arm64" ;;
        armv7l)        arch="arm" ;;
        *)             err "不支持的架构"; return 1 ;;
    esac

    local filename="hynode-linux-${arch}.tar.gz"
    local url="https://github.com/${REPO}/releases/download/${version}/${filename}"

    local was_running=false
    check_running && was_running=true && systemctl stop "${SERVICE_NAME}"

    info "下载 ${filename}..."
    wget -q -O "/tmp/${filename}" "$url" || { err "下载失败"; return 1; }
    tar -xzf "/tmp/${filename}" -C /tmp/
    mv /tmp/hynode "${BINARY}"
    chmod +x "${BINARY}"
    rm -f "/tmp/${filename}" /tmp/hynode

    ok "已更新到 ${version}"

    if [[ "$was_running" == true ]]; then
        systemctl start "${SERVICE_NAME}"
        sleep 1
        if check_running; then
            ok "HyNode 已重启"
        else
            err "重启失败，查看日志: hynode-cli log"
        fi
    fi
}

do_health() {
    local port
    port=$(grep -oP 'health_listen:\s*"\K[^"]+' "${CONFIG_FILE}" 2>/dev/null || echo "127.0.0.1:9090")
    curl -s "http://${port}/healthz" 2>/dev/null || err "无法连接健康检查端口"
}

show_help() {
    echo -e "${CYAN}HyNode 管理工具${PLAIN}"
    echo ""
    echo "用法: hynode-cli [命令]"
    echo ""
    echo "命令:"
    echo "  start       启动 HyNode"
    echo "  stop        停止 HyNode"
    echo "  restart     重启 HyNode"
    echo "  status      查看服务状态"
    echo "  log         查看实时日志"
    echo "  enable      启用开机自启"
    echo "  disable     禁用开机自启"
    echo "  config      编辑配置文件"
    echo "  generate    交互式生成配置"
    echo "  add         添加节点"
    echo "  del         删除节点"
    echo "  update      更新 HyNode"
    echo "  uninstall   卸载 HyNode"
    echo "  health      健康检查"
    echo "  help        显示此帮助"
    echo ""
}

show_menu() {
    echo ""
    echo -e "${CYAN}===== HyNode 管理 =====${PLAIN}"
    echo ""
    show_status
    echo "  1.  启动"
    echo "  2.  停止"
    echo "  3.  重启"
    echo "  4.  查看状态"
    echo "  5.  查看日志"
    echo "  6.  编辑配置"
    echo "  7.  生成配置"
    echo "  8.  添加节点"
    echo "  9.  删除节点"
    echo "  10. 更新"
    echo "  11. 健康检查"
    echo "  0.  退出"
    echo ""
    read -rp "请选择 [0-11]: " choice
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
        11) do_health ;;
        0)  exit 0 ;;
        *)  err "无效选择" ;;
    esac
    echo ""
    read -rp "按回车返回菜单..." _
    show_menu
}

# CLI dispatch
case "${1:-}" in
    start)     do_start ;;
    stop)      do_stop ;;
    restart)   do_restart ;;
    status)    do_status ;;
    log)       do_log ;;
    enable)    do_enable ;;
    disable)   do_disable ;;
    config)    do_config ;;
    generate)  do_generate ;;
    add)       do_add_node ;;
    del)       do_del_node ;;
    update)    do_update ;;
    uninstall) do_uninstall ;;
    health)    do_health ;;
    help|-h|--help) show_help ;;
    "")        show_menu ;;
    *)         err "未知命令: $1"; show_help; exit 1 ;;
esac
