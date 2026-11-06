#!/bin/bash
set -eu

SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
INSTALLER_PATH=$(cd "$SCRIPT_DIR/.." && pwd)/install-lite.sh
if grep -qi komari "$INSTALLER_PATH"; then
    echo "FAIL: install-lite.sh still mentions Komari" >&2
    grep -ni komari "$INSTALLER_PATH" >&2
    exit 1
fi
if grep -E '#!/bin/bash|\[''\[' "$INSTALLER_PATH" >/dev/null; then
    echo "FAIL: install-lite.sh still uses bash-only syntax" >&2
    grep -nE '#!/bin/bash|\[''\[' "$INSTALLER_PATH" >&2
    exit 1
fi
if ! grep -q '#!/bin/sh' "$INSTALLER_PATH"; then
    echo "FAIL: install-lite.sh is not a POSIX sh script" >&2
    exit 1
fi
if ! grep -q 'LITE_SERVICE_MANAGER=procd' "$INSTALLER_PATH"; then
    echo "FAIL: install-lite.sh does not create a procd service" >&2
    exit 1
fi
if [ "$(grep -c 'GODEBUG=disablethp=1' "$INSTALLER_PATH")" -lt 2 ]; then
    echo "FAIL: install-lite.sh does not disable transparent huge pages for systemd and procd" >&2
    exit 1
fi
if ! grep -q 'download_file' "$INSTALLER_PATH" || ! grep -q 'wget' "$INSTALLER_PATH"; then
    echo "FAIL: install-lite.sh does not support wget downloads" >&2
    exit 1
fi
if command -v sh >/dev/null 2>&1; then
    sh -n "$INSTALLER_PATH" || {
        echo "FAIL: install-lite.sh failed POSIX syntax check" >&2
        exit 1
    }
fi
LITE_INSTALLER_LIBRARY_ONLY=1
# shellcheck disable=SC1090
. "$INSTALLER_PATH"

fail() {
    echo "FAIL: $*" >&2
    exit 1
}

assert_file_content() {
    local path="$1"
    local want="$2"
    [ -f "$path" ] || fail "missing file: $path"
    [ "$(cat "$path")" = "$want" ] || fail "$path does not contain $want"
}

assert_file_contains() {
    local path="$1"
    local want="$2"
    [ -f "$path" ] || fail "missing file: $path"
    grep -q "$want" "$path" || fail "$path does not contain $want"
}

run_case() (
    set -eu
    local name="$1"
    local failure="${2:-}"
    local channel="${3:-stable}"
    local manager="${4:-systemd}"
    local root
    root=$(mktemp -d) || exit 1
    trap 'rm -rf "$root"' EXIT

    INSTALL_DIR="$root/opt"
    DATA_DIR="$INSTALL_DIR"
    BINARY_PATH="$INSTALL_DIR/Lite"
    ENV_FILE="$INSTALL_DIR/lite.env"
    SERVICE_NAME="lite-test"
    SERVICE_MANAGER="$manager"
    CHANNEL="$channel"
    TUI_TOOL=""
    mkdir -p "$INSTALL_DIR"
    printf old > "$BINARY_PATH"
    printf historical > "${BINARY_PATH}.backup.20250101_000000"

    local service_active=1
    local start_count=0
    local failed_backup_once=0
    local binary_executable=1
    local initd="$root/init.d/$SERVICE_NAME"
    mkdir -p "$root/init.d"

    is_installed() { [ -f "$BINARY_PATH" ]; }
    detect_service_manager() { printf '%s\n' "$manager"; }
    current_service_manager() { printf '%s\n' "$manager"; }
    service_available() { [ "$manager" = "systemd" ] || [ "$manager" = "procd" ]; }
    initd_script() { printf '%s\n' "$initd"; }
    select_channel() { CHANNEL="$channel"; }
    detect_arch() { printf amd64; }
    get_download_url() {
        printf '%s' "$CHANNEL" > "$root/seen-channel"
        if [ "$failure" = "url" ]; then
            return 1
        fi
            printf 'https://example.invalid/lite'
    }
    ui_msgbox() { :; }
    log_step() { :; }
    log_error() { :; }
    log_success() { :; }
    log_info() { :; }
    date() { printf 20260813_150000; }
    sleep() { :; }
    chmod() {
        local target="${*: -1}"
        if [ "$failure" = "chmod" ] && [[ "$target" == *lite-download* ]]; then
            binary_executable=0
            return 1
        fi
        binary_executable=1
    }
    cp() {
        if [ "$failure" = "backup" ] && [ "$failed_backup_once" -eq 0 ] && [[ "${2:-}" == *.backup.* ]]; then
            failed_backup_once=1
            return 1
        fi
        command cp "$@"
    }
    curl() {
        if [ "$failure" = "download" ]; then
            return 1
        fi
        local output=""
        while [ "$#" -gt 0 ]; do
            if [ "$1" = "-o" ]; then
                output="$2"
                shift 2
                continue
            fi
            shift
        done
        [ -n "$output" ] || return 1
        printf new > "$output"
    }
    wget() {
        if [ "$failure" = "download" ]; then
            return 1
        fi
        local output=""
        while [ "$#" -gt 0 ]; do
            if [ "$1" = "-O" ]; then
                output="$2"
                shift 2
                continue
            fi
            shift
        done
        [ -n "$output" ] || return 1
        printf new > "$output"
    }
    mv() {
        if [ "$failure" = "replace" ] && [[ "$*" == *lite-download* ]]; then
            return 1
        fi
        command mv "$@"
    }
    apply_service_action() {
        local action="$1"
        case "$action" in
            stop)
                if [ "$failure" = "stop" ]; then
                    service_active=0
                    return 1
                fi
                service_active=0
                ;;
            start|restart)
                start_count=$((start_count + 1))
                service_active=1
                if [ "$failure" = "start" ] && [ "$(cat "$BINARY_PATH" 2>/dev/null)" = "new" ]; then
                    service_active=0
                    return 1
                fi
                ;;
            is-active|running|status)
                [ "$service_active" -eq 1 ]
                ;;
            *) return 1 ;;
        esac
    }
    systemctl() { apply_service_action "$1"; }
    service_ctl() { apply_service_action "$1"; }
    service_is_active() { [ "$service_active" -eq 1 ]; }
    create_service() {
        printf '%s' "$1" > "$root/refreshed-port"
        mkdir -p "$INSTALL_DIR"
        write_env_file "$1"
    }
    service_enable() { :; }

    if [ "$failure" = "stop" ]; then
        upgrade_lite && fail "$name unexpectedly succeeded"
        assert_file_content "$BINARY_PATH" old
        [ "$service_active" -eq 1 ] || fail "$name stopped the original service"
    elif [ -n "$failure" ]; then
        upgrade_lite && fail "$name unexpectedly succeeded"
        assert_file_content "$BINARY_PATH" old
        [ "$binary_executable" -eq 1 ] || fail "$name restored a non-executable binary"
        [ "$service_active" -eq 1 ] || fail "$name did not leave the original service active"
    else
        upgrade_lite || fail "$name failed"
        assert_file_content "$BINARY_PATH" new
        [ "$binary_executable" -eq 1 ] || fail "$name installed a non-executable binary"
        [ "$service_active" -eq 1 ] || fail "$name did not leave the new service active"
        assert_file_content "$root/seen-channel" "$channel"
        assert_file_content "$root/refreshed-port" "$(read_listen_port)"
    fi

    assert_file_content "${BINARY_PATH}.backup.20250101_000000" historical
    if [ "$failure" != "stop" ] && [ "$failure" != "backup" ]; then
        assert_file_content "${BINARY_PATH}.backup.20260813_150000" old
    fi
    [ "$start_count" -le 2 ] || fail "$name restarted the service too many times"
    echo "PASS: $name"
)

run_download_case() (
    set -eu
    local root
    root=$(mktemp -d) || exit 1
    trap 'rm -rf "$root"' EXIT
    dest="$root/out"
    command() {
        if [ "$1" = "-v" ]; then
            if [ "$2" = "curl" ]; then
                return 1
            fi
            if [ "$2" = "wget" ]; then
                return 0
            fi
        fi
        return 1
    }
    wget() {
        local output=""
        while [ "$#" -gt 0 ]; do
            if [ "$1" = "-O" ]; then
                output="$2"
                shift 2
                continue
            fi
            shift
        done
        [ -n "$output" ] || return 1
        printf wget-ok > "$output"
    }
    download_file "https://example.invalid/lite" "$dest" || fail "wget fallback download failed"
    assert_file_content "$dest" wget-ok
    echo "PASS: wget download fallback"
)

run_procd_unit_case() (
    set -eu
    local root
    root=$(mktemp -d) || exit 1
    trap 'rm -rf "$root"' EXIT
    INSTALL_DIR="$root/opt"
    DATA_DIR="$INSTALL_DIR"
    BINARY_PATH="$INSTALL_DIR/Lite"
    ENV_FILE="$INSTALL_DIR/lite.env"
    SERVICE_NAME="lite"
    SERVICE_MANAGER="procd"
    mkdir -p "$INSTALL_DIR" "$root/init.d"
    initd_script() { printf '%s\n' "$root/init.d/lite"; }
    pid_file() { printf '%s\n' "$root/run/lite.pid"; }
    mkdir -p "$root/run"
    log_step() { :; }
    log_success() { :; }
    create_procd_service 36888
    write_env_file 36888
    script="$root/init.d/lite"
    assert_file_contains "$script" 'USE_PROCD=1'
    assert_file_contains "$script" 'LITE_DEPLOYMENT=linux'
    assert_file_contains "$script" 'LITE_SERVICE_MANAGER=procd'
    assert_file_contains "$script" 'GODEBUG=disablethp=1'
    assert_file_contains "$script" 'pidfile'
    assert_file_contains "$script" '0.0.0.0:${PORT}'
    assert_file_contains "$ENV_FILE" 'LISTEN_PORT=36888'
    [ -x "$script" ] || fail "procd init script is not executable"
    extracted=$(extract_listen_port_from_service)
    [ "$extracted" = "36888" ] || fail "extract_listen_port_from_service = $extracted"
    echo "PASS: procd unit template"
)

run_port_extract_case() (
    set -eu
    local root
    root=$(mktemp -d) || exit 1
    trap 'rm -rf "$root"' EXIT
    SERVICE_MANAGER="systemd"
    unit="$root/lite.service"
    systemd_unit_file() { printf '%s\n' "$unit"; }
    cat > "$unit" << 'EOF'
ExecStart=/opt/lite/Lite server -l 0.0.0.0:36888
Environment=LITE_LISTEN=0.0.0.0:36888
EOF
    extracted=$(extract_listen_port_from_service)
    [ "$extracted" = "36888" ] || fail "systemd extract_listen_port_from_service = $extracted"
    echo "PASS: systemd listen port extract"
)

run_case "stable upgrade" "" stable systemd
run_case "snapshot upgrade" "" snapshot systemd
run_case "procd upgrade" "" stable procd
run_case "stop failure" stop
run_case "backup failure" backup
run_case "download URL failure" url
run_case "download failure" download
run_case "chmod failure" chmod
run_case "replacement failure" replace
run_case "new service start failure" start
run_download_case
run_procd_unit_case
run_port_extract_case
