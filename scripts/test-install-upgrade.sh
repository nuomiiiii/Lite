#!/bin/bash
set -eu

SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
INSTALLER_PATH=$(cd "$SCRIPT_DIR/.." && pwd)/install-lite.sh
if grep -qi komari "$INSTALLER_PATH"; then
    echo "FAIL: install-lite.sh still mentions Komari" >&2
    grep -ni komari "$INSTALLER_PATH" >&2
    exit 1
fi
LITE_INSTALLER_LIBRARY_ONLY=1 source "$INSTALLER_PATH"

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

run_case() (
    set -eu
    local name="$1"
    local failure="${2:-}"
    local channel="${3:-stable}"
    local root
    root=$(mktemp -d) || exit 1
    trap 'rm -rf "$root"' EXIT

    INSTALL_DIR="$root/opt"
    DATA_DIR="$INSTALL_DIR"
    BINARY_PATH="$INSTALL_DIR/Lite"
    SERVICE_NAME="lite-test"
    CHANNEL="$channel"
    TUI_TOOL=""
    mkdir -p "$INSTALL_DIR"
    printf old > "$BINARY_PATH"
    printf historical > "${BINARY_PATH}.backup.20250101_000000"

    local service_active=1
    local start_count=0
    local failed_backup_once=0
    local binary_executable=1

    is_installed() { [ -f "$BINARY_PATH" ]; }
    check_systemd() { return 0; }
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
        if [ "$failure" = "chmod" ] && [[ "${*: -1}" == *.download.* ]]; then
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
    mv() {
        if [ "$failure" = "replace" ] && [[ "$*" == *download* ]]; then
            return 1
        fi
        command mv "$@"
    }
    systemctl() {
        local action="$1"
        case "$action" in
            stop)
                if [ "$failure" = "stop" ]; then
                    service_active=0
                    return 1
                fi
                service_active=0
                ;;
            start)
                start_count=$((start_count + 1))
                service_active=1
                if [ "$failure" = "start" ] && [ "$(cat "$BINARY_PATH" 2>/dev/null)" = "new" ]; then
                    service_active=0
                fi
                ;;
            is-active)
                [ "$service_active" -eq 1 ]
                ;;
            *) return 1 ;;
        esac
    }

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
    fi

    assert_file_content "${BINARY_PATH}.backup.20250101_000000" historical
    if [ "$failure" != "stop" ] && [ "$failure" != "backup" ]; then
        assert_file_content "${BINARY_PATH}.backup.20260813_150000" old
    fi
    [ "$start_count" -le 2 ] || fail "$name restarted the service too many times"
    echo "PASS: $name"
)

run_case "stable upgrade" "" stable
run_case "snapshot upgrade" "" snapshot
run_case "stop failure" stop
run_case "backup failure" backup
run_case "download URL failure" url
run_case "download failure" download
run_case "chmod failure" chmod
run_case "replacement failure" replace
run_case "new service start failure" start
