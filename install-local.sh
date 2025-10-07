#!/usr/bin/env bash

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# shellcheck disable=SC1090
source "${SCRIPT_DIR}/install.sh"

cleanup_tmp_dir() {
    if [ -n "${TMP_WORKDIR:-}" ] && [ -d "$TMP_WORKDIR" ]; then
        rm -rf "$TMP_WORKDIR"
    fi
}

ensure_go_installed() {
    if ! command -v go > /dev/null 2>&1; then
        echo "Go compiler not found. Please install Go 1.24 or newer from https://go.dev/doc/install"
        exit 1
    fi
}

install_local_main() {
    INSTALL_NAME="cs"
    SOURCE_DIR="$SCRIPT_DIR"
    UPGRADE_MODE=false

    while [[ $# -gt 0 ]]; do
        case $1 in
            --name)
                INSTALL_NAME="$2"
                shift 2
                ;;
            --source)
                SOURCE_DIR="$2"
                shift 2
                ;;
            *)
                echo "Unknown option: $1"
                echo "Usage: install-local.sh [--source <path>] [--name <n>]"
                exit 1
                ;;
        esac
    done

    SOURCE_DIR="$(cd "$SOURCE_DIR" && pwd)"

    check_command_exists
    detect_platform_and_arch
    check_and_install_dependencies
    ensure_go_installed
    setup_shell_and_path

    TMP_WORKDIR="$(mktemp -d)"
    trap cleanup_tmp_dir EXIT

    local build_target="${TMP_WORKDIR}/agent-squad${EXTENSION}"
    ensure go build -C "$SOURCE_DIR" -o "$build_target" .

    place_binary "$build_target" "$BIN_DIR" "$EXTENSION"

    cleanup_tmp_dir
    trap - EXIT
}

install_local_main "$@"
