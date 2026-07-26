#!/usr/bin/env bash
set -Eeuo pipefail

readonly SERVICE_NAME="hx-proxygroup.service"
readonly SERVICE_USER="hx-proxygroup"
readonly SERVICE_GROUP="hx-proxygroup"
readonly INSTALL_ROOT="/usr/local/lib/hx-proxygroup"
readonly BINARY_LINK="/usr/local/bin/hx-proxygroupd"
readonly UNIT_PATH="/etc/systemd/system/${SERVICE_NAME}"
readonly CONFIG_DIR="/etc/hx-proxygroup"
readonly DATA_DIR="/var/lib/hx-proxygroup"
readonly LOG_DIR="/var/log/hx-proxygroup"

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
ACTION="install"
VERSION="dev"
BINARY_SOURCE=""
CONFIRM_PURGE="false"
START_SERVICE="true"

log() {
    printf '[hx-proxygroup] %s\n' "$*"
}

warn() {
    printf '[hx-proxygroup] warning: %s\n' "$*" >&2
}

fail() {
    printf '[hx-proxygroup] error: %s\n' "$*" >&2
    exit 1
}

usage() {
    cat <<'USAGE'
Usage:
  sudo ./install.sh [install|upgrade|repair|status|uninstall|purge] [options]

Options:
  --version VERSION     Version directory and build version. Default: dev
  --binary PATH         Install an existing hx-proxygroupd binary instead of building.
  --no-start            Install files without starting the service.
  --confirm-purge       Required for purge; deletes configuration and persistent data.
  -h, --help            Show this help.

Current milestone installs the Go control plane only. The Mihomo data-plane unit
will be installed when data-plane integration is implemented.
USAGE
}

parse_arguments() {
    if [[ $# -gt 0 && "${1}" != --* ]]; then
        ACTION="$1"
        shift
    fi
    while [[ $# -gt 0 ]]; do
        case "$1" in
            --version)
                [[ $# -ge 2 ]] || fail "--version requires a value"
                VERSION="$2"
                shift 2
                ;;
            --binary)
                [[ $# -ge 2 ]] || fail "--binary requires a path"
                BINARY_SOURCE="$2"
                shift 2
                ;;
            --no-start)
                START_SERVICE="false"
                shift
                ;;
            --confirm-purge)
                CONFIRM_PURGE="true"
                shift
                ;;
            -h|--help)
                usage
                exit 0
                ;;
            *)
                fail "unknown option: $1"
                ;;
        esac
    done
    case "${ACTION}" in
        install|upgrade|repair|status|uninstall|purge) ;;
        *) fail "unsupported action: ${ACTION}" ;;
    esac
    [[ "${VERSION}" =~ ^[A-Za-z0-9._-]+$ ]] || fail "version may contain only letters, digits, dot, underscore, and hyphen"
}

require_root() {
    [[ "${EUID}" -eq 0 ]] || fail "this action must run as root"
}

require_command() {
    command -v "$1" >/dev/null 2>&1 || fail "required command not found: $1"
}

check_platform() {
    [[ "$(uname -s)" == "Linux" ]] || fail "only Linux is supported"
    [[ -d /run/systemd/system ]] || fail "systemd is required"
    case "$(uname -m)" in
        x86_64|amd64|aarch64|arm64) ;;
        *) warn "architecture $(uname -m) has not been validated" ;;
    esac
    require_command systemctl
    require_command install
    require_command getent
}

nologin_shell() {
    if [[ -x /usr/sbin/nologin ]]; then
        printf '%s\n' /usr/sbin/nologin
    elif [[ -x /sbin/nologin ]]; then
        printf '%s\n' /sbin/nologin
    else
        printf '%s\n' /bin/false
    fi
}

ensure_service_account() {
    if ! getent group "${SERVICE_GROUP}" >/dev/null; then
        groupadd --system "${SERVICE_GROUP}"
    fi
    if ! id -u "${SERVICE_USER}" >/dev/null 2>&1; then
        useradd \
            --system \
            --gid "${SERVICE_GROUP}" \
            --home-dir "${DATA_DIR}" \
            --shell "$(nologin_shell)" \
            --comment "HX-ProxyGroup service account" \
            "${SERVICE_USER}"
    fi
}

ensure_directories() {
    install -d -m 0750 -o root -g "${SERVICE_GROUP}" "${CONFIG_DIR}"
    install -d -m 0700 -o "${SERVICE_USER}" -g "${SERVICE_GROUP}" "${DATA_DIR}"
    install -d -m 0700 -o "${SERVICE_USER}" -g "${SERVICE_GROUP}" "${DATA_DIR}/runtime"
    install -d -m 0700 -o "${SERVICE_USER}" -g "${SERVICE_GROUP}" "${DATA_DIR}/snapshots"
    install -d -m 0700 -o "${SERVICE_USER}" -g "${SERVICE_GROUP}" "${DATA_DIR}/artifacts"
    install -d -m 0750 -o "${SERVICE_USER}" -g "${SERVICE_GROUP}" "${LOG_DIR}"
    if [[ ! -e "${CONFIG_DIR}/config.yaml" ]]; then
        printf '%s\n' '# HX-ProxyGroup configuration placeholder; structured config loading is not implemented yet.' \
            >"${CONFIG_DIR}/config.yaml"
        chmod 0640 "${CONFIG_DIR}/config.yaml"
        chown root:"${SERVICE_GROUP}" "${CONFIG_DIR}/config.yaml"
    fi
}

build_or_copy_binary() {
    local destination="$1"
    if [[ -n "${BINARY_SOURCE}" ]]; then
        [[ -f "${BINARY_SOURCE}" ]] || fail "binary does not exist: ${BINARY_SOURCE}"
        [[ -x "${BINARY_SOURCE}" ]] || fail "binary is not executable: ${BINARY_SOURCE}"
        install -m 0755 "${BINARY_SOURCE}" "${destination}"
        return
    fi

    require_command go
    local build_dir
    build_dir="$(mktemp -d)"
    trap 'rm -rf -- "${build_dir:-}"' RETURN
    log "building hx-proxygroupd version ${VERSION}"
    (
        cd "${SCRIPT_DIR}"
        CGO_ENABLED=0 go build \
            -trimpath \
            -ldflags "-s -w -X main.version=${VERSION}" \
            -o "${build_dir}/hx-proxygroupd" \
            ./cmd/hx-proxygroupd
    )
    install -m 0755 "${build_dir}/hx-proxygroupd" "${destination}"
    rm -rf -- "${build_dir}"
    trap - RETURN
}

install_unit() {
    local unit_source="${SCRIPT_DIR}/deploy/systemd/${SERVICE_NAME}"
    [[ -f "${unit_source}" ]] || fail "systemd unit not found: ${unit_source}"
    install -m 0644 "${unit_source}" "${UNIT_PATH}"
    systemctl daemon-reload
}

wait_until_ready() {
    local attempts=30
    local i
    for ((i = 1; i <= attempts; i++)); do
        if systemctl is-active --quiet "${SERVICE_NAME}"; then
            if command -v curl >/dev/null 2>&1; then
                if curl --fail --silent --show-error --max-time 1 \
                    http://127.0.0.1:19090/health/ready >/dev/null 2>&1; then
                    return 0
                fi
            else
                return 0
            fi
        fi
        sleep 1
    done
    return 1
}

install_or_upgrade() {
    require_root
    check_platform
    ensure_service_account
    ensure_directories

    local version_dir="${INSTALL_ROOT}/versions/${VERSION}"
    local candidate="${version_dir}/hx-proxygroupd"
    local old_target=""
    if [[ -L "${BINARY_LINK}" ]]; then
        old_target="$(readlink -f "${BINARY_LINK}" || true)"
    fi

    install -d -m 0755 -o root -g root "${version_dir}"
    build_or_copy_binary "${candidate}"
    ln -sfn "${candidate}" "${BINARY_LINK}"
    install_unit

    if [[ "${START_SERVICE}" == "false" ]]; then
        log "installed ${VERSION}; service start skipped"
        return
    fi

    systemctl enable "${SERVICE_NAME}" >/dev/null
    if ! systemctl restart "${SERVICE_NAME}"; then
        rollback_binary "${old_target}"
        fail "failed to restart ${SERVICE_NAME}"
    fi
    if ! wait_until_ready; then
        systemctl status "${SERVICE_NAME}" --no-pager || true
        journalctl -u "${SERVICE_NAME}" -n 50 --no-pager || true
        rollback_binary "${old_target}"
        fail "service did not become ready; previous binary restored when available"
    fi
    log "installed ${VERSION}; service is ready on 127.0.0.1:19090"
}

rollback_binary() {
    local old_target="$1"
    if [[ -n "${old_target}" && -x "${old_target}" ]]; then
        warn "restoring previous binary ${old_target}"
        ln -sfn "${old_target}" "${BINARY_LINK}"
        systemctl restart "${SERVICE_NAME}" || true
    else
        warn "no previous binary is available for rollback"
        systemctl stop "${SERVICE_NAME}" || true
    fi
}

repair_installation() {
    require_root
    check_platform
    [[ -x "${BINARY_LINK}" ]] || fail "${BINARY_LINK} is missing; run install first"
    ensure_service_account
    ensure_directories
    install_unit
    if [[ "${START_SERVICE}" == "true" ]]; then
        systemctl enable --now "${SERVICE_NAME}"
        wait_until_ready || fail "service did not become ready after repair"
    fi
    log "installation repaired"
}

show_status() {
    require_command systemctl
    systemctl status "${SERVICE_NAME}" --no-pager || true
    if [[ -L "${BINARY_LINK}" ]]; then
        log "active binary: $(readlink -f "${BINARY_LINK}")"
    fi
    log "configuration: ${CONFIG_DIR}"
    log "persistent data: ${DATA_DIR}"
}

uninstall_program() {
    require_root
    check_platform
    systemctl disable --now "${SERVICE_NAME}" >/dev/null 2>&1 || true
    rm -f -- "${UNIT_PATH}" "${BINARY_LINK}"
    systemctl daemon-reload
    systemctl reset-failed "${SERVICE_NAME}" >/dev/null 2>&1 || true
    log "program removed; configuration and persistent data were preserved"
}

purge_all() {
    require_root
    [[ "${CONFIRM_PURGE}" == "true" ]] || fail "purge requires --confirm-purge"
    uninstall_program
    rm -rf -- "${INSTALL_ROOT}" "${CONFIG_DIR}" "${DATA_DIR}" "${LOG_DIR}"
    if id -u "${SERVICE_USER}" >/dev/null 2>&1; then
        userdel "${SERVICE_USER}" || true
    fi
    if getent group "${SERVICE_GROUP}" >/dev/null; then
        groupdel "${SERVICE_GROUP}" || true
    fi
    log "program, configuration, persistent data, and service account purged"
}

parse_arguments "$@"

case "${ACTION}" in
    install|upgrade) install_or_upgrade ;;
    repair) repair_installation ;;
    status) show_status ;;
    uninstall) uninstall_program ;;
    purge) purge_all ;;
esac
