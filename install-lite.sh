#!/bin/sh

# POSIX installer for systemd Linux and OpenWrt/procd soft routers.
# Soft routers usually lack bash and cannot run `curl | bash`.

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[0;33m'
NC='\033[0m'

log_info() {
    printf '%s\n' "$1"
}

log_success() {
    printf '%b\n' "${GREEN}$1${NC}"
}

log_error() {
    printf '%b\n' "${RED}$1${NC}"
}

log_step() {
    printf '%b\n' "${YELLOW}$1${NC}"
}

INSTALL_DIR="/opt/lite"
DATA_DIR="/opt/lite"
SERVICE_NAME="lite"
BINARY_PATH="$INSTALL_DIR/Lite"
ENV_FILE="$INSTALL_DIR/lite.env"
DEFAULT_PORT="27777"
LISTEN_PORT=""
REPO="nuomiiiii/lite"
CHANNEL="stable"
TUI_TOOL=""
SERVICE_MANAGER=""

detect_tui() {
    if command -v whiptail >/dev/null 2>&1; then
        TUI_TOOL="whiptail"
    elif command -v dialog >/dev/null 2>&1; then
        TUI_TOOL="dialog"
    else
        TUI_TOOL=""
    fi
}

tui_enabled() {
    [ -n "$TUI_TOOL" ]
}

ui_menu() {
    title="$1"
    shift
    prompt="$1"
    shift

    if tui_enabled; then
        $TUI_TOOL --title "$title" --menu "$prompt" 20 70 10 "$@" 3>&1 1>&2 2>&3
        return $?
    fi

    {
        echo
        echo "=============================================================="
        echo "  $title"
        echo "=============================================================="
        echo "$prompt"
        echo
        while [ $# -gt 0 ]; do
            echo "  $1) $2"
            shift
            [ $# -gt 0 ] && shift
        done
        echo
    } >&2
    printf '输入选项: ' >&2
    read -r choice
    printf '%s\n' "$choice"
}

ui_input() {
    title="$1"
    prompt="$2"
    default="$3"

    if tui_enabled; then
        $TUI_TOOL --title "$title" --inputbox "$prompt" 12 70 "$default" 3>&1 1>&2 2>&3
        return $?
    fi

    printf '%s [默认: %s]: ' "$prompt" "$default" >&2
    read -r input
    if [ -z "$input" ]; then
        printf '%s\n' "$default"
    else
        printf '%s\n' "$input"
    fi
}

ui_yesno() {
    title="$1"
    prompt="$2"

    if tui_enabled; then
        $TUI_TOOL --title "$title" --yesno "$prompt" 12 70
        return $?
    fi

    printf '%s (Y/n): ' "$prompt" >&2
    read -r confirm
    case "$confirm" in
        [Nn]|[Nn][Oo]) return 1 ;;
        *) return 0 ;;
    esac
}

ui_msgbox() {
    title="$1"
    content="$2"

    if tui_enabled; then
        $TUI_TOOL --title "$title" --msgbox "$content" 20 72
        return
    fi

    echo
    echo "=============================================================="
    echo "  $title"
    echo "=============================================================="
    printf '%b\n' "$content"
    echo "=============================================================="
    printf '按回车键继续...' >&2
    read -r _
}

show_banner() {
    if tui_enabled; then
        return
    fi
    clear 2>/dev/null || true
    echo "=============================================================="
    echo "            轻量.Lite Monitoring System Installer"
    echo "       https://github.com/nuomiiiii/lite"
    echo "=============================================================="
    echo
}

select_channel() {
    choice=$(ui_menu "选择发布通道" "请选择要使用的发布通道：" \
        "stable" "稳定版 (推荐)" \
        "snapshot" "快照版 (最新功能)")

    case "$choice" in
        snapshot|2) CHANNEL="snapshot" ;;
        *) CHANNEL="stable" ;;
    esac
    log_info "已选择通道: $CHANNEL"
}

check_root() {
    if [ "$(id -u)" -ne 0 ]; then
        log_error "请使用 root 权限运行此脚本"
        exit 1
    fi
}

detect_service_manager() {
    if [ -n "$SERVICE_MANAGER" ]; then
        printf '%s\n' "$SERVICE_MANAGER"
        return
    fi
    if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
        printf '%s\n' "systemd"
        return
    fi
    if [ -f /etc/openwrt_release ] || [ -f /etc/openwrt_version ] || [ -x /sbin/procd ]; then
        printf '%s\n' "procd"
        return
    fi
    if command -v systemctl >/dev/null 2>&1; then
        printf '%s\n' "systemd"
        return
    fi
    printf '%s\n' "none"
}

current_service_manager() {
    if [ -n "$SERVICE_MANAGER" ]; then
        printf '%s\n' "$SERVICE_MANAGER"
        return
    fi
    SERVICE_MANAGER=$(detect_service_manager)
    printf '%s\n' "$SERVICE_MANAGER"
}

service_available() {
    case "$(current_service_manager)" in
        systemd|procd) return 0 ;;
        *) return 1 ;;
    esac
}

service_unit() {
    printf '%s.service\n' "$SERVICE_NAME"
}

systemd_unit_file() {
    printf '/etc/systemd/system/%s\n' "$(service_unit)"
}

initd_script() {
    printf '/etc/init.d/%s\n' "$SERVICE_NAME"
}

pid_file() {
    printf '/var/run/%s.pid\n' "$SERVICE_NAME"
}

service_ctl() {
    action="$1"
    manager=$(current_service_manager)
    case "$manager" in
        systemd)
            systemctl "$action" "$(service_unit)"
            ;;
        procd)
            "$(initd_script)" "$action"
            ;;
        *)
            return 1
            ;;
    esac
}

service_is_active() {
    manager=$(current_service_manager)
    case "$manager" in
        systemd)
            systemctl is-active --quiet "$(service_unit)"
            ;;
        procd)
            script=$(initd_script)
            if [ -x "$script" ]; then
                "$script" running >/dev/null 2>&1 || "$script" status >/dev/null 2>&1
            else
                return 1
            fi
            ;;
        *)
            return 1
            ;;
    esac
}

wait_for_service_active() {
    attempt=1
    while [ "$attempt" -le 10 ]; do
        if service_is_active; then
            sleep 2
            service_is_active && return 0
        fi
        sleep 1
        attempt=$((attempt + 1))
    done
    return 1
}

service_enable() {
    manager=$(current_service_manager)
    case "$manager" in
        systemd)
            systemctl daemon-reload
            systemctl enable "$(service_unit)"
            ;;
        procd)
            "$(initd_script)" enable
            ;;
        *)
            return 1
            ;;
    esac
}

detect_arch() {
    arch=$(uname -m)
    case "$arch" in
        x86_64|amd64) printf '%s\n' "amd64" ;;
        aarch64|arm64) printf '%s\n' "arm64" ;;
        i386|i686) printf '%s\n' "386" ;;
        riscv64) printf '%s\n' "riscv64" ;;
        loongarch64|loong64) printf '%s\n' "loong64" ;;
        *)
            log_error "不支持的架构: $arch"
            log_error "当前官方 Linux 包提供 amd64 / arm64 / 386 / riscv64 / loong64。"
            return 1
            ;;
    esac
}

is_installed() {
    [ -f "$BINARY_PATH" ]
}

has_downloader() {
    command -v curl >/dev/null 2>&1 || command -v wget >/dev/null 2>&1 || command -v uclient-fetch >/dev/null 2>&1
}

install_dependencies() {
    log_step "检查并安装依赖..."
    if has_downloader; then
        return 0
    fi
    if command -v opkg >/dev/null 2>&1; then
        log_info "使用 opkg 安装 wget 与证书..."
        opkg update
        opkg install ca-bundle >/dev/null 2>&1 || opkg install ca-certificates >/dev/null 2>&1 || true
        opkg install wget >/dev/null 2>&1 || opkg install wget-ssl >/dev/null 2>&1 || opkg install uclient-fetch >/dev/null 2>&1 || true
    elif command -v apt >/dev/null 2>&1; then
        log_info "使用 apt 安装 curl..."
        apt update
        apt install -y curl ca-certificates
    elif command -v yum >/dev/null 2>&1; then
        log_info "使用 yum 安装 curl..."
        yum install -y curl ca-certificates
    elif command -v apk >/dev/null 2>&1; then
        log_info "使用 apk 安装 curl..."
        apk add curl ca-certificates
    else
        log_error "未找到 curl 或 wget，且没有可用的包管理器 (opkg/apt/yum/apk)"
        return 1
    fi
    has_downloader
}

download_file() {
    url="$1"
    dest="$2"
    if command -v curl >/dev/null 2>&1; then
        curl -fL --retry 3 -o "$dest" "$url"
    elif command -v wget >/dev/null 2>&1; then
        wget -O "$dest" "$url"
    elif command -v uclient-fetch >/dev/null 2>&1; then
        uclient-fetch -O "$dest" "$url"
    else
        return 1
    fi
}

download_stdout() {
    url="$1"
    if command -v curl >/dev/null 2>&1; then
        curl -fsSL --retry 3 "$url"
    elif command -v wget >/dev/null 2>&1; then
        wget -qO- "$url"
    elif command -v uclient-fetch >/dev/null 2>&1; then
        uclient-fetch -O- "$url"
    else
        return 1
    fi
}

get_download_url() {
    arch="$1"
    file_name="Lite-linux-${arch}"

    if [ "$CHANNEL" = "snapshot" ]; then
        log_info "获取最新 snapshot 版本..." >&2
        latest_snapshot=$(download_stdout "https://api.github.com/repos/${REPO}/releases" | grep '"tag_name"' | grep 'Snapshot-' | head -n 1 | sed -e 's/.*"tag_name": *"//' -e 's/".*//')
        if [ -z "$latest_snapshot" ]; then
            log_error "未找到 snapshot 版本" >&2
            return 1
        fi
        log_info "最新 snapshot 版本: $latest_snapshot" >&2
        printf 'https://github.com/%s/releases/download/%s/%s\n' "$REPO" "$latest_snapshot" "$file_name"
    else
        printf 'https://github.com/%s/releases/latest/download/%s\n' "$REPO" "$file_name"
    fi
}

write_env_file() {
    port="$1"
    mkdir -p "$INSTALL_DIR"
    cat > "$ENV_FILE" << EOF
LISTEN_PORT=${port}
EOF
}

read_listen_port() {
    if [ -n "$LISTEN_PORT" ]; then
        printf '%s\n' "$LISTEN_PORT"
        return
    fi
    if [ -f "$ENV_FILE" ]; then
        # shellcheck disable=SC1090
        . "$ENV_FILE"
        if [ -n "$LISTEN_PORT" ]; then
            printf '%s\n' "$LISTEN_PORT"
            return
        fi
    fi
    extracted=$(extract_listen_port_from_service)
    if [ -n "$extracted" ]; then
        printf '%s\n' "$extracted"
        return
    fi
    printf '%s\n' "$DEFAULT_PORT"
}

extract_listen_port_from_service() {
    manager=$(current_service_manager)
    case "$manager" in
        systemd)
            unit=$(systemd_unit_file)
            if [ -f "$unit" ]; then
                sed -n 's/.*0\.0\.0\.0:\([0-9][0-9]*\).*/\1/p' "$unit" | head -n 1
            fi
            ;;
        procd)
            script=$(initd_script)
            if [ -f "$script" ]; then
                sed -n 's/^PORT="\([0-9][0-9]*\)".*/\1/p' "$script" | head -n 1
            fi
            ;;
    esac
}

create_systemd_service() {
    port="$1"
    log_step "创建 systemd 服务..."
    service_file=$(systemd_unit_file)
    cat > "$service_file" << EOF
[Unit]
Description=轻量.Lite Service
After=network.target

[Service]
Type=simple
ExecStart=${BINARY_PATH} server -l 0.0.0.0:${port}
WorkingDirectory=${DATA_DIR}
Restart=always
User=root
Environment=LITE_DEPLOYMENT=linux
Environment=LITE_SERVICE_NAME=$(service_unit)
Environment=LITE_SERVICE_MANAGER=systemd
Environment=LITE_LISTEN=0.0.0.0:${port}
Environment=GODEBUG=disablethp=1

[Install]
WantedBy=multi-user.target
EOF
    log_success "systemd 服务文件创建完成"
}

create_procd_service() {
    port="$1"
    log_step "创建 OpenWrt / procd 服务..."
    script=$(initd_script)
    cat > "$script" << EOF
#!/bin/sh /etc/rc.common

START=99
STOP=10
USE_PROCD=1

PROG="${BINARY_PATH}"
DATA_DIR="${DATA_DIR}"
PORT="${port}"
PIDFILE="$(pid_file)"

start_service() {
	[ -f "${ENV_FILE}" ] && . "${ENV_FILE}"
	[ -n "\$LISTEN_PORT" ] && PORT="\$LISTEN_PORT"
	procd_open_instance
	procd_set_param command "\$PROG" server -l "0.0.0.0:\${PORT}"
	procd_set_param cwd "\$DATA_DIR"
	procd_set_param env LITE_DEPLOYMENT=linux LITE_SERVICE_NAME=${SERVICE_NAME} LITE_SERVICE_MANAGER=procd LITE_LISTEN=0.0.0.0:\${PORT} GODEBUG=disablethp=1
	procd_set_param pidfile "\$PIDFILE"
	procd_set_param respawn 3600 5 5
	procd_set_param stdout 1
	procd_set_param stderr 1
	procd_close_instance
}
EOF
    chmod +x "$script"
    log_success "procd 服务脚本创建完成"
}

create_service() {
    port="$1"
    write_env_file "$port"
    manager=$(current_service_manager)
    case "$manager" in
        systemd) create_systemd_service "$port" ;;
        procd) create_procd_service "$port" ;;
        *)
            log_error "未检测到 systemd 或 OpenWrt procd，已跳过服务创建"
            return 1
            ;;
    esac
}

detect_ip() {
    ip_addr=""
    if command -v ip >/dev/null 2>&1; then
        ip_addr=$(ip -4 route get 1 2>/dev/null | awk '{for (i = 1; i <= NF; i++) if ($i == "src") { print $(i + 1); exit }}')
    fi
    if [ -z "$ip_addr" ] && command -v hostname >/dev/null 2>&1; then
        ip_addr=$(hostname -I 2>/dev/null | awk '{print $1}')
    fi
    if [ -z "$ip_addr" ]; then
        ip_addr="<路由器 IP>"
    fi
    printf '%s\n' "$ip_addr"
}

show_access_info() {
    port=${1:-$DEFAULT_PORT}
    ip=$(detect_ip)
    manager=$(current_service_manager)
    content="安装完成！\n\n"
    content="${content}访问信息：\n"
    content="${content}  URL: http://${ip}:${port}\n"
    content="${content}\n首次使用请访问上述地址，按安装向导创建管理员账号。\n"
    content="${content}\n服务管理命令：\n"
    case "$manager" in
        systemd)
            content="${content}  状态: systemctl status $SERVICE_NAME\n"
            content="${content}  启动: systemctl start $SERVICE_NAME\n"
            content="${content}  停止: systemctl stop $SERVICE_NAME\n"
            content="${content}  重启: systemctl restart $SERVICE_NAME\n"
            content="${content}  日志: journalctl -u $SERVICE_NAME -f"
            ;;
        procd)
            content="${content}  状态: /etc/init.d/$SERVICE_NAME status\n"
            content="${content}  启动: /etc/init.d/$SERVICE_NAME start\n"
            content="${content}  停止: /etc/init.d/$SERVICE_NAME stop\n"
            content="${content}  重启: /etc/init.d/$SERVICE_NAME restart\n"
            content="${content}  日志: logread -e Lite"
            ;;
    esac
    ui_msgbox "安装完成" "$content"
}

ask_listen_port() {
    while true; do
        input_port=$(ui_input "监听端口" "请输入 轻量.Lite 的监听端口 (1-65535)：" "$DEFAULT_PORT")
        if [ $? -ne 0 ]; then
            log_info "安装已取消"
            return 1
        fi
        case "$input_port" in
            "")
                LISTEN_PORT="$DEFAULT_PORT"
                return 0
                ;;
            *[!0-9]*)
                ui_msgbox "错误" "端口号无效，请输入 1-65535 之间的数字。"
                ;;
            *)
                if [ "$input_port" -ge 1 ] && [ "$input_port" -le 65535 ]; then
                    LISTEN_PORT="$input_port"
                    return 0
                fi
                ui_msgbox "错误" "端口号无效，请输入 1-65535 之间的数字。"
                ;;
        esac
    done
}

install_binary() {
    log_step "开始二进制安装..."

    if is_installed; then
        ui_msgbox "提示" "轻量.Lite 已安装。\n如需升级，请使用主菜单中的升级选项。"
        return
    fi

    select_channel
    ask_listen_port || return

    if ! install_dependencies; then
        ui_msgbox "错误" "安装下载工具失败。软路由请先执行：opkg update && opkg install wget ca-bundle"
        return 1
    fi

    arch=$(detect_arch) || return 1
    log_info "检测到架构: $arch"
    log_info "服务管理: $(current_service_manager)"

    log_step "创建安装目录: $INSTALL_DIR"
    mkdir -p "$INSTALL_DIR"
    log_step "创建数据目录: $DATA_DIR"
    mkdir -p "$DATA_DIR"

    download_url=$(get_download_url "$arch") || {
        ui_msgbox "错误" "获取下载链接失败，请检查网络连接或稍后重试。"
        return 1
    }

    log_step "下载 轻量.Lite 二进制文件..."
    log_info "URL: $download_url"
    if ! download_file "$download_url" "$BINARY_PATH"; then
        ui_msgbox "错误" "下载失败，请检查网络连接。"
        return 1
    fi

    chmod +x "$BINARY_PATH"
    log_success "轻量.Lite 二进制文件安装完成: $BINARY_PATH"

    if ! service_available; then
        ui_msgbox "安装完成" "警告：未检测到 systemd 或 OpenWrt procd，已跳过服务创建。\n\n您可以手动运行 轻量.Lite：\n    $BINARY_PATH server -l 0.0.0.0:$LISTEN_PORT"
        return
    fi

    create_service "$LISTEN_PORT" || return 1
    service_enable
    service_ctl start

    if wait_for_service_active; then
        log_success "轻量.Lite 服务启动成功"
        show_access_info "$LISTEN_PORT"
    else
        ui_msgbox "错误" "轻量.Lite 服务启动失败。请查看系统日志。"
        return 1
    fi
}

restore_upgrade_backup() {
    rm -f "$download_path"
    if ! cp "$backup_path" "$BINARY_PATH"; then
        log_error "恢复原二进制文件失败: $backup_path"
        return 1
    fi
    chmod +x "$BINARY_PATH" || return 1
    service_ctl start || return 1
    wait_for_service_active
}

upgrade_lite() {
    log_step "升级 轻量.Lite..."

    if ! is_installed; then
        ui_msgbox "错误" "轻量.Lite 未安装。请先安装它。"
        return 1
    fi

    if ! service_available; then
        ui_msgbox "错误" "未检测到 systemd 或 OpenWrt procd。无法管理服务。"
        return 1
    fi

    select_channel

    log_step "停止 轻量.Lite 服务..."
    if ! service_ctl stop; then
        if service_ctl start && wait_for_service_active; then
            ui_msgbox "错误" "停止 轻量.Lite 服务失败，升级已取消并已确认原服务正常运行。"
        else
            ui_msgbox "错误" "停止 轻量.Lite 服务失败，升级已取消，但原服务状态异常，请检查服务日志。"
        fi
        return 1
    fi

    log_step "备份当前二进制文件..."
    backup_path="${BINARY_PATH}.backup.$(date +%Y%m%d_%H%M%S)"
    if [ -e "$backup_path" ]; then
        backup_path="${backup_path}.$$"
    fi
    if ! cp "$BINARY_PATH" "$backup_path"; then
        log_error "备份当前二进制文件失败，升级已取消"
        if service_ctl start && wait_for_service_active; then
            ui_msgbox "错误" "备份当前版本失败，升级已取消并已重新启动原服务。"
        else
            ui_msgbox "错误" "备份当前版本失败，升级已取消，但原服务未能重新启动，请检查服务日志。"
        fi
        return 1
    fi

    download_path=$(mktemp /tmp/lite-download.XXXXXX)
    if [ $? -ne 0 ] || [ -z "$download_path" ]; then
        log_error "创建下载临时文件失败，升级已取消"
        if service_ctl start && wait_for_service_active; then
            ui_msgbox "错误" "无法创建下载临时文件，升级已取消并已重新启动原服务。"
        else
            ui_msgbox "错误" "无法创建下载临时文件，升级已取消，但原服务未能重新启动，请检查服务日志。"
        fi
        return 1
    fi

    arch=$(detect_arch)
    if [ $? -ne 0 ] || [ -z "$arch" ]; then
        log_error "检测系统架构失败，正在从本次备份恢复"
        if restore_upgrade_backup; then
            ui_msgbox "错误" "检测系统架构失败，已从本次备份恢复。"
        else
            ui_msgbox "错误" "检测系统架构失败，且原版本恢复后未能正常运行，请检查服务日志。"
        fi
        return 1
    fi

    download_url=$(get_download_url "$arch")
    if [ $? -ne 0 ] || [ -z "$download_url" ]; then
        log_error "获取下载链接失败，正在从备份恢复"
        if restore_upgrade_backup; then
            ui_msgbox "错误" "获取下载链接失败，已从本次备份恢复。"
        else
            ui_msgbox "错误" "获取下载链接失败，且原版本恢复后未能正常运行，请检查服务日志。"
        fi
        return 1
    fi

    log_step "下载最新版本..."
    if ! download_file "$download_url" "$download_path"; then
        log_error "下载失败，正在从备份恢复"
        if restore_upgrade_backup; then
            ui_msgbox "错误" "下载失败，已从本次备份恢复。"
        else
            ui_msgbox "错误" "下载失败，且原版本恢复后未能正常运行，请检查服务日志。"
        fi
        return 1
    fi

    if ! chmod +x "$download_path" || ! mv -f "$download_path" "$BINARY_PATH"; then
        log_error "安装新版本失败，正在从本次备份恢复"
        if restore_upgrade_backup; then
            ui_msgbox "错误" "安装新版本失败，已从本次备份恢复。"
        else
            ui_msgbox "错误" "安装新版本失败，且原版本恢复后未能正常运行，请检查服务日志。"
        fi
        return 1
    fi

    log_step "刷新服务配置..."
    if ! create_service "$(read_listen_port)"; then
        log_error "刷新服务配置失败，正在从本次备份恢复"
        if restore_upgrade_backup; then
            ui_msgbox "错误" "刷新服务配置失败，已从本次备份恢复。"
        else
            ui_msgbox "错误" "刷新服务配置失败，且原版本恢复后未能正常运行，请检查服务日志。"
        fi
        return 1
    fi
    service_enable >/dev/null 2>&1 || true

    log_step "重启 轻量.Lite 服务..."
    service_ctl start

    if wait_for_service_active; then
        ui_msgbox "升级完成" "轻量.Lite 升级成功 (通道: $CHANNEL)。后台一键更新已启用。"
    else
        log_error "新版本服务未能启动，正在从本次备份恢复"
        if restore_upgrade_backup; then
            ui_msgbox "错误" "新版本未能启动，已恢复并重新启动原版本。"
        else
            ui_msgbox "错误" "新版本未能启动，且原版本恢复后仍未正常运行，请检查服务日志。"
        fi
        return 1
    fi
}

uninstall_lite() {
    log_step "卸载 轻量.Lite..."

    if ! is_installed; then
        ui_msgbox "提示" "轻量.Lite 未安装。"
        return 0
    fi

    if ! ui_yesno "确认卸载" "这将删除 轻量.Lite 二进制文件和服务。\n\n您确定要继续吗？"; then
        log_info "卸载已取消"
        return 0
    fi

    if service_available; then
        log_step "停止并禁用服务..."
        service_ctl stop >/dev/null 2>&1 || true
        manager=$(current_service_manager)
        case "$manager" in
            systemd)
                systemctl disable "$(service_unit)" >/dev/null 2>&1 || true
                rm -f "$(systemd_unit_file)"
                systemctl daemon-reload
                log_success "systemd 服务已删除"
                ;;
            procd)
                script=$(initd_script)
                if [ -x "$script" ]; then
                    "$script" disable >/dev/null 2>&1 || true
                    rm -f "$script"
                fi
                rm -f "$(pid_file)"
                log_success "procd 服务已删除"
                ;;
        esac
    fi

    log_step "删除二进制文件..."
    rm -f "$BINARY_PATH"
    rmdir "$INSTALL_DIR" 2>/dev/null || log_info "数据目录 $INSTALL_DIR 不为空，未删除"
    log_success "轻量.Lite 二进制文件已删除"
    ui_msgbox "卸载完成" "轻量.Lite 卸载完成。\n\n数据文件保留在 $DATA_DIR"
}

show_status() {
    if ! is_installed; then
        ui_msgbox "错误" "轻量.Lite 未安装。"
        return
    fi
    if ! service_available; then
        ui_msgbox "错误" "未检测到 systemd 或 OpenWrt procd。无法获取服务状态。"
        return
    fi
    manager=$(current_service_manager)
    if tui_enabled; then
        case "$manager" in
            systemd) status_output=$(systemctl status "$(service_unit)" --no-pager -l 2>&1) ;;
            procd) status_output=$("$(initd_script)" status 2>&1) ;;
        esac
        ui_msgbox "服务状态" "$status_output"
    else
        log_step "轻量.Lite 服务状态:"
        case "$manager" in
            systemd) systemctl status "$(service_unit)" --no-pager -l ;;
            procd) "$(initd_script)" status ;;
        esac
        printf '按回车键继续...' >&2
        read -r _
    fi
}

show_logs() {
    if ! is_installed; then
        ui_msgbox "错误" "轻量.Lite 未安装。"
        return
    fi
    if ! service_available; then
        ui_msgbox "错误" "未检测到 systemd 或 OpenWrt procd。无法获取服务日志。"
        return
    fi
    if tui_enabled; then
        clear 2>/dev/null || true
    fi
    log_step "查看 轻量.Lite 服务日志 (按 Ctrl+C 退出)..."
    manager=$(current_service_manager)
    case "$manager" in
        systemd) journalctl -u "$SERVICE_NAME" -f --no-pager ;;
        procd)
            if command -v logread >/dev/null 2>&1; then
                logread -f -e Lite
            else
                log_error "未找到 logread"
            fi
            ;;
    esac
}

restart_service() {
    if ! is_installed; then
        ui_msgbox "错误" "轻量.Lite 未安装。"
        return
    fi
    if ! service_available; then
        ui_msgbox "错误" "未检测到 systemd 或 OpenWrt procd。无法重启服务。"
        return
    fi
    log_step "重启 轻量.Lite 服务..."
    service_ctl restart 2>/dev/null || {
        service_ctl stop >/dev/null 2>&1 || true
        service_ctl start
    }
    if wait_for_service_active; then
        ui_msgbox "成功" "服务重启成功。"
    else
        ui_msgbox "错误" "服务重启失败，请检查日志。"
    fi
}

stop_service() {
    if ! is_installed; then
        ui_msgbox "错误" "轻量.Lite 未安装。"
        return
    fi
    if ! service_available; then
        ui_msgbox "错误" "未检测到 systemd 或 OpenWrt procd。无法停止服务。"
        return
    fi
    log_step "停止 轻量.Lite 服务..."
    service_ctl stop
    ui_msgbox "成功" "服务已停止。"
}

main_menu() {
    while true; do
        show_banner
        choice=$(ui_menu "轻量.Lite 监控系统安装器" "请选择操作：" \
            "1" "安装 轻量.Lite" \
            "2" "升级 轻量.Lite" \
            "3" "卸载 轻量.Lite" \
            "4" "查看状态" \
            "5" "查看日志" \
            "6" "重启服务" \
            "7" "停止服务" \
            "8" "退出")

        if [ $? -ne 0 ] && tui_enabled; then
            clear 2>/dev/null || true
            exit 0
        fi

        case $choice in
            1) install_binary ;;
            2) upgrade_lite ;;
            3) uninstall_lite ;;
            4) show_status ;;
            5) show_logs ;;
            6) restart_service ;;
            7) stop_service ;;
            8)
                tui_enabled && clear 2>/dev/null || true
                exit 0
                ;;
            *) ui_msgbox "错误" "无效选项" ;;
        esac

        if ! tui_enabled; then
            break
        fi
    done
}

if [ "${LITE_INSTALLER_LIBRARY_ONLY:-0}" != "1" ]; then
    check_root
    detect_tui
    SERVICE_MANAGER=$(detect_service_manager)
    main_menu
fi
