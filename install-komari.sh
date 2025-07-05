#!/bin/bash

# Color definitions for terminal output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[0;33m'
BLUE='\033[0;34m'
PURPLE='\033[0;35m'
CYAN='\033[0;36m'
WHITE='\033[1;37m'
NC='\033[0m' # No Color

# Logging functions
log_info() {
    echo -e "${NC} $1"
}

log_success() {
    echo -e "${GREEN}✓${NC} $1"
}

log_warning() {
    echo -e "${YELLOW}⚠${NC} $1"
}

log_error() {
    echo -e "${RED}✗${NC} $1"
}

log_step() {
    echo -e "${BLUE}→${NC} $1"
}

log_config() {
    echo -e "${NC} $1"
}

# Default configuration
DEFAULT_PORT="25774"
DEFAULT_SERVICE_NAME="komari"
DEFAULT_INSTALL_DIR="/opt/komari"
DEFAULT_DOCKER_VOLUME="$(pwd)/data"

# Global variables
INSTALL_METHOD=""
PORT="$DEFAULT_PORT"
SERVICE_NAME="$DEFAULT_SERVICE_NAME"
INSTALL_DIR="$DEFAULT_INSTALL_DIR"
DOCKER_VOLUME="$DEFAULT_DOCKER_VOLUME"

# Show banner
show_banner() {
    clear
    echo -e "${WHITE}"
    echo "╔══════════════════════════════════════════════════════════════╗"
    echo "║                    Komari 监控系统                            ║"
    echo "║                   一键安装管理脚本                             ║"
    echo "║                                                              ║"
    echo "║            https://github.com/komari-monitor/komari          ║"
    echo "╚══════════════════════════════════════════════════════════════╝"
    echo -e "${NC}"
    echo
}

# Check if running as root
check_root() {
    if [ "$EUID" -ne 0 ]; then
        log_error "请使用 root 权限运行此脚本"
        log_info "请执行: sudo bash $0"
        exit 1
    fi
}

# Detect system architecture
detect_arch() {
    local arch=$(uname -m)
    case $arch in
        x86_64)
            echo "amd64"
            ;;
        aarch64)
            echo "arm64"
            ;;
        i386|i686)
            echo "386"
            ;;
        riscv64)
            echo "riscv64"
            ;;
        *)
            log_error "不支持的架构: $arch"
            exit 1
            ;;
    esac
}

# Check if Docker is installed
check_docker() {
    if command -v docker >/dev/null 2>&1; then
        return 0
    else
        return 1
    fi
}

# Check if Komari is already installed
check_installation() {
    local binary_installed=false
    local docker_installed=false
    local service_installed=false

    # Check binary installation
    if [ -f "$INSTALL_DIR/komari" ]; then
        binary_installed=true
    fi

    # Check Docker installation
    if check_docker && docker ps -a --format "table {{.Names}}" | grep -q "^komari$"; then
        docker_installed=true
    fi

    # Check systemd service
    if command -v systemctl >/dev/null 2>&1 && systemctl list-unit-files | grep -q "${SERVICE_NAME}.service"; then
        service_installed=true
    fi

    echo "$binary_installed $docker_installed $service_installed"
}

# Install dependencies
install_dependencies() {
    log_step "检查并安装依赖..."

    local deps="curl"
    local missing_deps=""
    
    for cmd in $deps; do
        if ! command -v $cmd >/dev/null 2>&1; then
            missing_deps="$missing_deps $cmd"
        fi
    done

    if [ -n "$missing_deps" ]; then
        if command -v apt >/dev/null 2>&1; then
            log_info "使用 apt 安装依赖..."
            apt update
            apt install -y $missing_deps
        elif command -v yum >/dev/null 2>&1; then
            log_info "使用 yum 安装依赖..."
            yum install -y $missing_deps
        elif command -v apk >/dev/null 2>&1; then
            log_info "使用 apk 安装依赖..."
            apk add $missing_deps
        else
            log_error "未找到支持的包管理器 (apt/yum/apk)"
            exit 1
        fi
    fi
}

# Install Docker if not present
install_docker() {
    if ! check_docker; then
        log_step "正在安装 Docker..."
        curl -fsSL https://get.docker.com | sh
        systemctl enable docker
        systemctl start docker
        
        if ! check_docker; then
            log_error "Docker 安装失败"
            return 1
        fi
        log_success "Docker 安装成功"
    else
        log_success "Docker 已安装"
    fi
}

# Get user input for configuration
get_config() {
    echo
    log_config "配置安装参数："
    
    # Port configuration
    read -p "请输入监听端口 (默认: $DEFAULT_PORT): " input_port
    PORT=${input_port:-$DEFAULT_PORT}
    
    if [ "$INSTALL_METHOD" = "binary" ]; then
        # Service name for binary installation
        read -p "请输入服务名称 (默认: $DEFAULT_SERVICE_NAME): " input_service
        SERVICE_NAME=${input_service:-$DEFAULT_SERVICE_NAME}
        
        # Install directory
        read -p "请输入安装目录 (默认: $DEFAULT_INSTALL_DIR): " input_dir
        INSTALL_DIR=${input_dir:-$DEFAULT_INSTALL_DIR}
    elif [ "$INSTALL_METHOD" = "docker" ]; then
        # Docker volume path
        read -p "请输入数据目录 (默认: $DEFAULT_DOCKER_VOLUME): " input_volume
        DOCKER_VOLUME=${input_volume:-$DEFAULT_DOCKER_VOLUME}
    fi
    
    echo
    log_config "配置确认："
    log_config "  安装方式: ${GREEN}$INSTALL_METHOD${NC}"
    log_config "  监听端口: ${GREEN}$PORT${NC}"
    
    if [ "$INSTALL_METHOD" = "binary" ]; then
        log_config "  服务名称: ${GREEN}$SERVICE_NAME${NC}"
        log_config "  安装目录: ${GREEN}$INSTALL_DIR${NC}"
    elif [ "$INSTALL_METHOD" = "docker" ]; then
        log_config "  数据目录: ${GREEN}$DOCKER_VOLUME${NC}"
    fi
    
    echo
    read -p "确认以上配置？(y/N): " confirm
    if [[ ! $confirm =~ ^[Yy]$ ]]; then
        log_info "配置取消，返回主菜单"
        return 1
    fi
    return 0
}

# Binary installation
install_binary() {
    log_step "开始二进制安装..."
    
    # Install dependencies
    install_dependencies
    
    # Detect architecture
    local arch=$(detect_arch)
    log_info "检测到架构: ${GREEN}$arch${NC}"
    
    # Create installation directory
    log_step "创建安装目录: ${GREEN}$INSTALL_DIR${NC}"
    mkdir -p "$INSTALL_DIR"
    
    # Download binary
    local file_name="komari-linux-${arch}"
    local download_url="https://github.com/komari-monitor/komari/releases/latest/download/${file_name}"
    local binary_path="${INSTALL_DIR}/komari"
    
    log_step "下载 Komari 二进制文件..."
    log_info "URL: ${CYAN}$download_url${NC}"
    
    if ! curl -L -o "$binary_path" "$download_url"; then
        log_error "下载失败"
        return 1
    fi
    
    # Set executable permissions
    chmod +x "$binary_path"
    log_success "Komari 二进制文件安装完成: ${GREEN}$binary_path${NC}"
    
    # Create systemd service
    create_systemd_service
    
    # Start service
    systemctl daemon-reload
    systemctl enable ${SERVICE_NAME}.service
    systemctl start ${SERVICE_NAME}.service
    
    if systemctl is-active --quiet ${SERVICE_NAME}.service; then
        log_success "Komari 服务启动成功"
        show_binary_info
        show_access_info
    else
        log_error "Komari 服务启动失败"
        log_info "查看日志: journalctl -u ${SERVICE_NAME} -f"
        return 1
    fi
}

# Create systemd service file
create_systemd_service() {
    log_step "创建 systemd 服务..."
    
    local service_file="/etc/systemd/system/${SERVICE_NAME}.service"
    cat > "$service_file" << EOF
[Unit]
Description=Komari Monitor Service
After=network.target

[Service]
Type=simple
ExecStart=${INSTALL_DIR}/komari server -l 0.0.0.0:${PORT}
WorkingDirectory=${INSTALL_DIR}
Restart=always
User=root

[Install]
WantedBy=multi-user.target
EOF

    log_success "systemd 服务文件创建完成"
}

# Docker installation
install_docker() {
    log_step "开始 Docker 安装..."
    
    # Install Docker if needed
    if ! check_docker; then
        log_step "正在安装 Docker..."
        curl -fsSL https://get.docker.com | sh
        systemctl enable docker
        systemctl start docker
        
        if ! check_docker; then
            log_error "Docker 安装失败"
            return 1
        fi
        log_success "Docker 安装成功"
    fi
    
    # Create data directory
    mkdir -p "$DOCKER_VOLUME"
    log_success "数据目录创建完成: ${GREEN}$DOCKER_VOLUME${NC}"
    
    # Pull latest image
    log_step "拉取 Komari Docker 镜像..."
    if ! docker pull ghcr.io/komari-monitor/komari:latest; then
        log_error "镜像拉取失败"
        return 1
    fi
    
    # Stop and remove existing container
    if docker ps -a --format "table {{.Names}}" | grep -q "^komari$"; then
        log_step "停止并删除现有容器..."
        docker stop komari >/dev/null 2>&1
        docker rm komari >/dev/null 2>&1
    fi
    
    # Run new container
    log_step "启动 Komari 容器..."
    if ! docker run -d \
        -p ${PORT}:25774 \
        -v "${DOCKER_VOLUME}:/app/data" \
        --name komari \
        --restart unless-stopped \
        ghcr.io/komari-monitor/komari:latest; then
        log_error "容器启动失败"
        return 1
    fi
    
    # Wait for container to start
    sleep 3
    
    if docker ps --format "table {{.Names}}" | grep -q "^komari$"; then
        log_success "Komari 容器启动成功"
        show_docker_info
        show_access_info
    else
        log_error "Komari 容器启动失败"
        log_info "查看日志: docker logs komari"
        return 1
    fi
}

# Show Docker container info
show_docker_info() {
    echo
    log_config "Docker 容器信息："
    log_config "  容器名称: ${GREEN}komari${NC}"
    log_config "  数据目录: ${GREEN}$DOCKER_VOLUME${NC}"
    log_config "  端口映射: ${GREEN}$PORT:25774${NC}"
    
    log_step "获取默认登录信息..."
    sleep 2
    local logs=$(docker logs komari 2>&1 | grep -i "Default admin account created" | tail -1)
    if [ -n "$logs" ]; then
        echo
        log_success "默认登录信息："
        echo -e "${GREEN}$logs${NC}"
    else
        log_warning "未找到默认登录信息，请查看容器日志: docker logs komari"
    fi
}

# Show binary installation info
show_binary_info() {
    echo
    log_config "二进制安装信息："
    log_config "  安装目录: ${GREEN}$INSTALL_DIR${NC}"
    log_config "  服务名称: ${GREEN}$SERVICE_NAME${NC}"
    log_config "  监听端口: ${GREEN}$PORT${NC}"
    
    log_step "获取默认登录信息..."
    sleep 2
    local logs=$(journalctl -u ${SERVICE_NAME} --since "2 minutes ago" | grep -i "Default admin account created" | tail -1)
    if [ -n "$logs" ]; then
        echo
        log_success "默认登录信息："
        echo -e "${GREEN}$logs${NC}"
    else
        log_warning "未找到默认登录信息，请查看服务日志: journalctl -u ${SERVICE_NAME} -f"
    fi
}

# Show access information
show_access_info() {
    echo
    log_success "安装完成！"
    echo
    log_config "访问信息："
    log_config "  本地访问: ${GREEN}http://localhost:${PORT}${NC}"
    log_config "  外部访问: ${GREEN}http://$(hostname -I | awk '{print $1}'):${PORT}${NC}"
    echo
    
    if [ "$INSTALL_METHOD" = "binary" ]; then
        log_config "服务管理命令："
        log_config "  查看状态: ${GREEN}systemctl status $SERVICE_NAME${NC}"
        log_config "  启动服务: ${GREEN}systemctl start $SERVICE_NAME${NC}"
        log_config "  停止服务: ${GREEN}systemctl stop $SERVICE_NAME${NC}"
        log_config "  重启服务: ${GREEN}systemctl restart $SERVICE_NAME${NC}"
        log_config "  查看日志: ${GREEN}journalctl -u $SERVICE_NAME -f${NC}"
    elif [ "$INSTALL_METHOD" = "docker" ]; then
        log_config "Docker 管理命令："
        log_config "  查看状态: ${GREEN}docker ps${NC}"
        log_config "  启动容器: ${GREEN}docker start komari${NC}"
        log_config "  停止容器: ${GREEN}docker stop komari${NC}"
        log_config "  重启容器: ${GREEN}docker restart komari${NC}"
        log_config "  查看日志: ${GREEN}docker logs komari -f${NC}"
    fi
}

# Upgrade function
upgrade_komari() {
    local install_status=($(check_installation))
    local binary_installed=${install_status[0]}
    local docker_installed=${install_status[1]}
    local service_installed=${install_status[2]}
    
    echo
    log_step "检测当前安装状态..."
    
    if [ "$binary_installed" = "true" ]; then
        log_info "检测到二进制安装"
        INSTALL_METHOD="binary"
        upgrade_binary
    elif [ "$docker_installed" = "true" ]; then
        log_info "检测到 Docker 安装"
        INSTALL_METHOD="docker"
        upgrade_docker
    else
        log_warning "未检测到现有安装，请先安装 Komari"
        return 1
    fi
}

# Upgrade binary installation
upgrade_binary() {
    log_step "升级二进制安装..."
    
    # Get current configuration
    if systemctl is-active --quiet ${SERVICE_NAME}.service; then
        local service_file="/etc/systemd/system/${SERVICE_NAME}.service"
        if [ -f "$service_file" ]; then
            # Extract port from service file
            PORT=$(grep "ExecStart" "$service_file" | sed -n 's/.*-l 0\.0\.0\.0:\([0-9]*\).*/\1/p')
            # Extract install directory
            INSTALL_DIR=$(grep "ExecStart" "$service_file" | sed 's|/komari.*||' | awk '{print $NF}')
        fi
    fi
    
    log_config "当前配置："
    log_config "  安装目录: ${GREEN}$INSTALL_DIR${NC}"
    log_config "  监听端口: ${GREEN}$PORT${NC}"
    log_config "  服务名称: ${GREEN}$SERVICE_NAME${NC}"
    
    # Stop service
    log_step "停止 Komari 服务..."
    systemctl stop ${SERVICE_NAME}.service
    
    # Backup current binary
    if [ -f "${INSTALL_DIR}/komari" ]; then
        log_step "备份当前版本..."
        cp "${INSTALL_DIR}/komari" "${INSTALL_DIR}/komari.backup.$(date +%Y%m%d_%H%M%S)"
    fi
    
    # Download new version
    local arch=$(detect_arch)
    local file_name="komari-linux-${arch}"
    local download_url="https://github.com/komari-monitor/komari/releases/latest/download/${file_name}"
    local binary_path="${INSTALL_DIR}/komari"
    
    log_step "下载最新版本..."
    if ! curl -L -o "$binary_path" "$download_url"; then
        log_error "下载失败，恢复备份"
        if [ -f "${INSTALL_DIR}/komari.backup."* ]; then
            cp "${INSTALL_DIR}"/komari.backup.* "$binary_path"
        fi
        systemctl start ${SERVICE_NAME}.service
        return 1
    fi
    
    chmod +x "$binary_path"
    
    # Start service
    log_step "重启 Komari 服务..."
    systemctl start ${SERVICE_NAME}.service
    
    if systemctl is-active --quiet ${SERVICE_NAME}.service; then
        log_success "Komari 升级成功"
        
        # Show version info
        sleep 2
        local version_info=$(journalctl -u ${SERVICE_NAME} --since "1 minute ago" | grep "Komari Monitor" | tail -1)
        if [ -n "$version_info" ]; then
            log_info "版本信息: ${GREEN}$version_info${NC}"
        fi
        
        # Show login info if this is a fresh installation
        local login_info=$(journalctl -u ${SERVICE_NAME} --since "2 minutes ago" | grep -i "Default admin account created" | tail -1)
        if [ -n "$login_info" ]; then
            echo
            log_success "默认登录信息："
            echo -e "${GREEN}$login_info${NC}"
        fi
        
        show_access_info
    else
        log_error "服务启动失败，请检查日志"
        return 1
    fi
}

# Upgrade Docker installation
upgrade_docker() {
    log_step "升级 Docker 安装..."
    
    # Get current configuration
    if docker ps -a --format "table {{.Names}}\t{{.Ports}}" | grep komari; then
        local port_info=$(docker ps -a --format "table {{.Ports}}" --filter "name=komari" | grep -v PORTS)
        PORT=$(echo "$port_info" | sed -n 's/.*:\([0-9]*\)->25774.*/\1/p')
        
        local volume_info=$(docker inspect komari --format '{{range .Mounts}}{{if eq .Destination "/app/data"}}{{.Source}}{{end}}{{end}}')
        if [ -n "$volume_info" ]; then
            DOCKER_VOLUME="$volume_info"
        fi
    fi
    
    log_config "当前配置："
    log_config "  数据目录: ${GREEN}$DOCKER_VOLUME${NC}"
    log_config "  监听端口: ${GREEN}$PORT${NC}"
    
    # Pull latest image
    log_step "拉取最新镜像..."
    if ! docker pull ghcr.io/komari-monitor/komari:latest; then
        log_error "镜像拉取失败"
        return 1
    fi
    
    # Stop and remove current container
    log_step "停止当前容器..."
    docker stop komari >/dev/null 2>&1
    docker rm komari >/dev/null 2>&1
    
    # Start new container with same configuration
    log_step "启动新容器..."
    if ! docker run -d \
        -p ${PORT}:25774 \
        -v "${DOCKER_VOLUME}:/app/data" \
        --name komari \
        --restart unless-stopped \
        ghcr.io/komari-monitor/komari:latest; then
        log_error "容器启动失败"
        return 1
    fi
    
    sleep 3
    
    if docker ps --format "table {{.Names}}" | grep -q "^komari$"; then
        log_success "Komari 升级成功"
        show_access_info
    else
        log_error "容器启动失败"
        return 1
    fi
}

# Uninstall function
uninstall_komari() {
    local install_status=($(check_installation))
    local binary_installed=${install_status[0]}
    local docker_installed=${install_status[1]}
    local service_installed=${install_status[2]}
    
    echo
    log_step "检测当前安装状态..."
    
    local has_installation=false
    
    if [ "$binary_installed" = "true" ] || [ "$service_installed" = "true" ]; then
        log_warning "检测到二进制/服务安装"
        has_installation=true
    fi
    
    if [ "$docker_installed" = "true" ]; then
        log_warning "检测到 Docker 安装"
        has_installation=true
    fi
    
    if [ "$has_installation" = "false" ]; then
        log_info "未检测到 Komari 安装"
        return 0
    fi
    
    echo
    log_warning "警告：此操作将完全卸载 Komari"
    log_warning "数据文件将被保留，但服务和程序文件将被删除"
    echo
    read -p "确认卸载？(y/N): " confirm
    if [[ ! $confirm =~ ^[Yy]$ ]]; then
        log_info "卸载取消"
        return 0
    fi
    
    # Uninstall binary/service
    if [ "$binary_installed" = "true" ] || [ "$service_installed" = "true" ]; then
        log_step "卸载二进制安装..."
        
        # Stop and disable service
        if systemctl list-unit-files | grep -q "${SERVICE_NAME}.service"; then
            systemctl stop ${SERVICE_NAME}.service >/dev/null 2>&1
            systemctl disable ${SERVICE_NAME}.service >/dev/null 2>&1
            rm -f "/etc/systemd/system/${SERVICE_NAME}.service"
            systemctl daemon-reload
            log_success "systemd 服务已删除"
        fi
        
        # Remove binary
        if [ -f "$INSTALL_DIR/komari" ]; then
            rm -f "$INSTALL_DIR/komari"
            # Remove directory if empty
            rmdir "$INSTALL_DIR" 2>/dev/null
            log_success "二进制文件已删除"
        fi
    fi
    
    # Uninstall Docker
    if [ "$docker_installed" = "true" ]; then
        log_step "卸载 Docker 安装..."
        
        # Stop and remove container
        docker stop komari >/dev/null 2>&1
        docker rm komari >/dev/null 2>&1
        log_success "Docker 容器已删除"
        
        # Remove image (optional)
        read -p "是否删除 Docker 镜像？(y/N): " remove_image
        if [[ $remove_image =~ ^[Yy]$ ]]; then
            docker rmi ghcr.io/komari-monitor/komari:latest >/dev/null 2>&1
            log_success "Docker 镜像已删除"
        fi
    fi
    
    log_success "Komari 卸载完成"
    log_info "数据文件位置："
    if [ "$binary_installed" = "true" ]; then
        log_info "  二进制数据: ${GREEN}$INSTALL_DIR/data${NC} (如果存在)"
    fi
    if [ "$docker_installed" = "true" ]; then
        log_info "  Docker 数据: ${GREEN}$DOCKER_VOLUME${NC}"
    fi
}

# Main menu
show_menu() {
    local install_status=($(check_installation))
    local binary_installed=${install_status[0]}
    local docker_installed=${install_status[1]}
    local service_installed=${install_status[2]}
    
    echo
    log_config "当前状态："
    if [ "$binary_installed" = "true" ]; then
        log_config "  二进制安装: ${GREEN}已安装${NC}"
    else
        log_config "  二进制安装: ${RED}未安装${NC}"
    fi
    
    if [ "$docker_installed" = "true" ]; then
        log_config "  Docker 安装: ${GREEN}已安装${NC}"
    else
        log_config "  Docker 安装: ${RED}未安装${NC}"
    fi
    
    echo
    echo -e "${WHITE}请选择操作：${NC}"
    echo -e "${CYAN}1${NC}) 使用二进制文件安装"
    echo -e "${CYAN}2${NC}) 使用 Docker 安装"
    
    if [ "$binary_installed" = "true" ] || [ "$docker_installed" = "true" ]; then
        echo -e "${CYAN}3${NC}) 升级更新"
        echo -e "${CYAN}4${NC}) 卸载"
    fi
    
    echo -e "${CYAN}0${NC}) 退出脚本"
    echo
}

# Main script logic
main() {
    # Check root privileges
    check_root
    
    while true; do
        show_banner
        show_menu
        
        read -p "请输入选项 [0-4]: " choice
        
        case $choice in
            1)
                INSTALL_METHOD="binary"
                if get_config; then
                    install_binary
                fi
                echo
                read -p "按 Enter 继续..."
                ;;
            2)
                INSTALL_METHOD="docker"
                if get_config; then
                    install_docker
                fi
                echo
                read -p "按 Enter 继续..."
                ;;
            3)
                upgrade_komari
                echo
                read -p "按 Enter 继续..."
                ;;
            4)
                uninstall_komari
                echo
                read -p "按 Enter 继续..."
                ;;
            0)
                echo
                log_info "感谢使用 Komari 安装脚本！"
                exit 0
                ;;
            *)
                log_error "无效选项，请重新选择"
                sleep 2
                ;;
        esac
    done
}

# Run main function
main "$@"
