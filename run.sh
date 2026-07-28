#!/usr/bin/env bash
set -Eeuo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
VERSION="dev"
LISTEN_ADDRESS="127.0.0.1:19090"
DATA_DIR="${SCRIPT_DIR}/.tmp/run-data"
RUN_DIR="${SCRIPT_DIR}/.tmp/run"
FRONTEND_DIR="${SCRIPT_DIR}/web"
FRONTEND_HOST="127.0.0.1"
FRONTEND_PORT="5173"
MIHOMO_BINARY="mihomo"
BINARY_SOURCE=""
BACKEND_ONLY="false"
REQUIRE_FRONTEND="false"
INSTALL_FRONTEND_DEPS="true"

BACKEND_BINARY=""
BACKEND_PID=""
FRONTEND_PID=""

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
  ./run.sh [options]

Starts HX-ProxyGroup locally without root, systemd, service users, or files under
/usr/local, /etc, /var/lib, and /var/log. Ctrl+C stops all child processes.

Options:
  --version VERSION          Backend build version. Default: dev
  --binary PATH              Run an existing hx-proxygroupd binary.
  --listen ADDRESS           Backend listen address. Default: 127.0.0.1:19090
  --data-dir PATH            Local runtime data. Default: ./.tmp/run-data
  --frontend-dir PATH        React project directory. Default: ./web
  --frontend-host HOST       Frontend development host. Default: 127.0.0.1
  --frontend-port PORT       Frontend development port. Default: 5173
  --mihomo PATH              Mihomo executable path or command name. Default: mihomo
  --backend-only             Do not discover or start the frontend.
  --require-frontend         Fail when frontend/package.json is absent.
  --install-frontend-deps    Install local frontend dependencies when missing (default).
  --no-install-frontend-deps Fail instead of installing when node_modules is missing.
  -h, --help                 Show this help.

The React frontend is discovered from web/package.json and starts automatically.
When dependencies are missing, run.sh installs them from the lock file before
starting Vite. If the requested frontend port is occupied, the next available
port within the following 20 ports is selected and printed.
USAGE
}

require_command() {
    command -v "$1" >/dev/null 2>&1 || fail "required command not found: $1"
}

absolute_path() {
    local value="$1"
    if [[ "${value}" == /* ]]; then
        printf '%s\n' "${value}"
    else
        printf '%s\n' "${SCRIPT_DIR}/${value#./}"
    fi
}

parse_arguments() {
    while [[ $# -gt 0 ]]; do
        case "$1" in
            --version)
                [[ $# -ge 2 ]] || fail "--version requires a value"
                VERSION="$2"
                shift 2
                ;;
            --binary)
                [[ $# -ge 2 ]] || fail "--binary requires a path"
                BINARY_SOURCE="$(absolute_path "$2")"
                shift 2
                ;;
            --listen)
                [[ $# -ge 2 ]] || fail "--listen requires a value"
                LISTEN_ADDRESS="$2"
                shift 2
                ;;
            --data-dir)
                [[ $# -ge 2 ]] || fail "--data-dir requires a value"
                DATA_DIR="$(absolute_path "$2")"
                shift 2
                ;;
            --frontend-dir)
                [[ $# -ge 2 ]] || fail "--frontend-dir requires a value"
                FRONTEND_DIR="$(absolute_path "$2")"
                shift 2
                ;;
            --frontend-host)
                [[ $# -ge 2 ]] || fail "--frontend-host requires a value"
                FRONTEND_HOST="$2"
                shift 2
                ;;
            --frontend-port)
                [[ $# -ge 2 ]] || fail "--frontend-port requires a value"
                FRONTEND_PORT="$2"
                shift 2
                ;;
            --mihomo)
                [[ $# -ge 2 ]] || fail "--mihomo requires a value"
                MIHOMO_BINARY="$2"
                shift 2
                ;;
            --backend-only)
                BACKEND_ONLY="true"
                shift
                ;;
            --require-frontend)
                REQUIRE_FRONTEND="true"
                shift
                ;;
            --install-frontend-deps)
                INSTALL_FRONTEND_DEPS="true"
                shift
                ;;
            --no-install-frontend-deps)
                INSTALL_FRONTEND_DEPS="false"
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

    [[ "${VERSION}" =~ ^[A-Za-z0-9._-]+$ ]] || fail "version contains unsupported characters"
    [[ -n "${MIHOMO_BINARY// }" ]] || fail "mihomo executable cannot be empty"
    [[ "${FRONTEND_PORT}" =~ ^[0-9]+$ ]] || fail "frontend port must be an integer"
    ((FRONTEND_PORT >= 1 && FRONTEND_PORT <= 65535)) || fail "frontend port must be between 1 and 65535"
    if [[ "${BACKEND_ONLY}" == "true" && "${REQUIRE_FRONTEND}" == "true" ]]; then
        fail "--backend-only and --require-frontend cannot be used together"
    fi
}

prepare_local_directories() {
    mkdir -p \
        "${RUN_DIR}/bin" \
        "${DATA_DIR}/artifacts" \
        "${DATA_DIR}/runtime" \
        "${DATA_DIR}/snapshots" \
        "${DATA_DIR}/state" \
        "${DATA_DIR}/tmp"
    chmod 0700 \
        "${RUN_DIR}" \
        "${RUN_DIR}/bin" \
        "${DATA_DIR}" \
        "${DATA_DIR}/artifacts" \
        "${DATA_DIR}/runtime" \
        "${DATA_DIR}/snapshots" \
        "${DATA_DIR}/state" \
        "${DATA_DIR}/tmp"

    local config_path="${RUN_DIR}/config.yaml"
    if [[ ! -e "${config_path}" ]]; then
        printf '%s\n' '# Local run placeholder; structured configuration loading is not implemented yet.' >"${config_path}"
        chmod 0600 "${config_path}"
    fi
}

build_or_copy_backend() {
    BACKEND_BINARY="${RUN_DIR}/bin/hx-proxygroupd"
    if [[ -n "${BINARY_SOURCE}" ]]; then
        [[ -f "${BINARY_SOURCE}" ]] || fail "binary does not exist: ${BINARY_SOURCE}"
        [[ -x "${BINARY_SOURCE}" ]] || fail "binary is not executable: ${BINARY_SOURCE}"
        cp -- "${BINARY_SOURCE}" "${BACKEND_BINARY}"
        chmod 0755 "${BACKEND_BINARY}"
        return
    fi

    require_command go
    log "building backend version ${VERSION}"
    (
        cd "${SCRIPT_DIR}"
        CGO_ENABLED=0 go build \
            -trimpath \
            -ldflags "-s -w -X main.version=${VERSION}" \
            -o "${BACKEND_BINARY}" \
            ./cmd/hx-proxygroupd
    )
    chmod 0755 "${BACKEND_BINARY}"
}

start_backend() {
    local binary="$1"
    log "starting backend at http://${LISTEN_ADDRESS}"
    "${binary}" \
        --listen "${LISTEN_ADDRESS}" \
        --data-dir "${DATA_DIR}" \
        --config "${RUN_DIR}/config.yaml" \
        --database "${DATA_DIR}/hx-proxygroup.db" \
        --master-key "${DATA_DIR}/master.key" \
        --runtime-config "${DATA_DIR}/runtime/active.yaml" \
        --snapshots "${DATA_DIR}/snapshots" \
        --mihomo "${MIHOMO_BINARY}" &
    BACKEND_PID="$!"
}

wait_for_backend() {
    local health_url="http://${LISTEN_ADDRESS}/health/ready"
    local attempt
    if ! command -v curl >/dev/null 2>&1; then
        warn "curl is unavailable; backend readiness check skipped"
        sleep 1
        kill -0 "${BACKEND_PID}" 2>/dev/null || fail "backend exited during startup"
        return
    fi

    for ((attempt = 1; attempt <= 30; attempt++)); do
        if ! kill -0 "${BACKEND_PID}" 2>/dev/null; then
            wait "${BACKEND_PID}" || true
            fail "backend exited before becoming ready"
        fi
        if curl --fail --silent --show-error --max-time 1 "${health_url}" >/dev/null 2>&1; then
            log "backend is ready"
            return
        fi
        sleep 1
    done
    fail "backend did not become ready at ${health_url}"
}

detect_frontend_package_manager() {
    if [[ -f "${FRONTEND_DIR}/pnpm-lock.yaml" ]]; then
        printf '%s\n' pnpm
    elif [[ -f "${FRONTEND_DIR}/yarn.lock" ]]; then
        printf '%s\n' yarn
    elif [[ -f "${FRONTEND_DIR}/bun.lock" || -f "${FRONTEND_DIR}/bun.lockb" ]]; then
        printf '%s\n' bun
    else
        printf '%s\n' npm
    fi
}

install_frontend_dependencies() {
    local package_manager="$1"
    log "installing frontend dependencies with ${package_manager}"
    case "${package_manager}" in
        pnpm)
            (cd "${FRONTEND_DIR}" && pnpm install --frozen-lockfile)
            ;;
        yarn)
            (cd "${FRONTEND_DIR}" && yarn install --frozen-lockfile)
            ;;
        bun)
            (cd "${FRONTEND_DIR}" && bun install --frozen-lockfile)
            ;;
        npm)
            if [[ -f "${FRONTEND_DIR}/package-lock.json" ]]; then
                (cd "${FRONTEND_DIR}" && npm ci)
            else
                (cd "${FRONTEND_DIR}" && npm install)
            fi
            ;;
        *)
            fail "unsupported frontend package manager: ${package_manager}"
            ;;
    esac
}

start_frontend_if_available() {
    if [[ "${BACKEND_ONLY}" == "true" ]]; then
        log "frontend disabled by --backend-only"
        return
    fi
    if [[ ! -f "${FRONTEND_DIR}/package.json" ]]; then
        if [[ "${REQUIRE_FRONTEND}" == "true" ]]; then
            fail "frontend package.json not found: ${FRONTEND_DIR}/package.json"
        fi
        warn "frontend package.json is missing; backend continues without the management UI"
        return
    fi

    local package_manager
    package_manager="$(detect_frontend_package_manager)"
    require_command "${package_manager}"
    if [[ ! -d "${FRONTEND_DIR}/node_modules" ]]; then
        if [[ "${INSTALL_FRONTEND_DEPS}" != "true" ]]; then
            fail "frontend dependencies are missing; rerun with --install-frontend-deps"
        fi
        install_frontend_dependencies "${package_manager}"
    fi

    local requested_port="${FRONTEND_PORT}"
    local probe_host="${FRONTEND_HOST}"
    if [[ "${probe_host}" == "0.0.0.0" || "${probe_host}" == "::" ]]; then
        probe_host="127.0.0.1"
    fi
    local maximum_port=$((requested_port + 20))
    ((maximum_port > 65535)) && maximum_port=65535
    while ((FRONTEND_PORT <= maximum_port)); do
        if ! (exec 9<>"/dev/tcp/${probe_host}/${FRONTEND_PORT}") 2>/dev/null; then
            break
        fi
        FRONTEND_PORT=$((FRONTEND_PORT + 1))
    done
    if ((FRONTEND_PORT > maximum_port)); then
        fail "no available frontend port between ${requested_port} and ${maximum_port}"
    fi
    if [[ "${FRONTEND_PORT}" != "${requested_port}" ]]; then
        warn "frontend port ${requested_port} is in use; using ${FRONTEND_PORT} instead"
    fi

    local api_base="http://${LISTEN_ADDRESS}"
    log "starting frontend at http://${FRONTEND_HOST}:${FRONTEND_PORT}"
    (
        cd "${FRONTEND_DIR}"
        export VITE_BACKEND_TARGET="${api_base}"
        exec "${package_manager}" run dev -- --host "${FRONTEND_HOST}" --port "${FRONTEND_PORT}"
    ) &
    FRONTEND_PID="$!"
}

cleanup() {
    local status="$?"
    trap - EXIT INT TERM HUP

    if [[ -n "${FRONTEND_PID}" ]] && kill -0 "${FRONTEND_PID}" 2>/dev/null; then
        kill -TERM "${FRONTEND_PID}" 2>/dev/null || true
    fi
    if [[ -n "${BACKEND_PID}" ]] && kill -0 "${BACKEND_PID}" 2>/dev/null; then
        kill -TERM "${BACKEND_PID}" 2>/dev/null || true
    fi

    [[ -z "${FRONTEND_PID}" ]] || wait "${FRONTEND_PID}" 2>/dev/null || true
    [[ -z "${BACKEND_PID}" ]] || wait "${BACKEND_PID}" 2>/dev/null || true
    exit "${status}"
}

wait_for_children() {
    local status=0
    set +e
    if [[ -n "${FRONTEND_PID}" ]]; then
        wait -n "${BACKEND_PID}" "${FRONTEND_PID}"
        status="$?"
        warn "backend or frontend exited; stopping the remaining process"
    else
        wait "${BACKEND_PID}"
        status="$?"
    fi
    set -e
    return "${status}"
}

main() {
    parse_arguments "$@"
    prepare_local_directories
    build_or_copy_backend

    trap cleanup EXIT
    trap 'exit 130' INT
    trap 'exit 143' TERM HUP

    start_backend "${BACKEND_BINARY}"
    wait_for_backend
    start_frontend_if_available

    log "local data: ${DATA_DIR}"
    if [[ -n "${FRONTEND_PID}" ]]; then
        log "backend and frontend are running; press Ctrl+C to stop both"
    else
        log "backend is running; press Ctrl+C to stop"
    fi
    wait_for_children
}

main "$@"
