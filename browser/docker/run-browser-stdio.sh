#!/bin/sh
set -eu

LIGHTPANDA_PIDS=""

resolve_lightpanda_data_dir() {
  preferred="${LIGHTPANDA_DATA_DIR:-${TMPDIR:-/tmp}/lightpanda}"
  if mkdir -p "${preferred}" 2>/dev/null; then
    probe_file="${preferred}/.write-test-$$"
    if ( : >"${probe_file}" ) 2>/dev/null; then
      rm -f "${probe_file}" 2>/dev/null || true
      printf "%s" "${preferred}"
      return
    fi
  fi

  fallback="${TMPDIR:-/tmp}/lightpanda"
  mkdir -p "${fallback}"
  printf "%s" "${fallback}"
}

normalize_positive_int() {
  value="$1"
  fallback="$2"
  case "${value}" in
    ''|*[!0-9]*)
      printf "%s" "${fallback}"
      return
      ;;
  esac
  if [ "${value}" -lt 1 ]; then
    printf "%s" "${fallback}"
    return
  fi
  printf "%s" "${value}"
}

cleanup() {
  for pid in ${LIGHTPANDA_PIDS}; do
    if kill -0 "${pid}" 2>/dev/null; then
      kill "${pid}" 2>/dev/null || true
      wait "${pid}" 2>/dev/null || true
    fi
  done
}

trap cleanup EXIT INT TERM

LIGHTPANDA_DATA_DIR_RESOLVED="$(resolve_lightpanda_data_dir)"
LIGHTPANDA_CACHE_DIR_RESOLVED="${LIGHTPANDA_CACHE_DIR:-${LIGHTPANDA_DATA_DIR_RESOLVED}/cache}"
mkdir -p "${LIGHTPANDA_CACHE_DIR_RESOLVED}" 2>/dev/null || true

LIGHTPANDA_POOL_SIZE_RAW="${LIGHTPANDA_POOL_SIZE:-${BROWSER_BROWSER_POOL_SIZE:-1}}"
LIGHTPANDA_POOL_SIZE_NORMALIZED="$(normalize_positive_int "${LIGHTPANDA_POOL_SIZE_RAW}" 1)"
LIGHTPANDA_BASE_PORT_RAW="${LIGHTPANDA_PORT:-9222}"
LIGHTPANDA_BASE_PORT="$(normalize_positive_int "${LIGHTPANDA_BASE_PORT_RAW}" 9222)"
LIGHTPANDA_HOST_RESOLVED="${LIGHTPANDA_HOST:-127.0.0.1}"

if [ -x "/opt/lightpanda/lightpanda" ]; then
  i=0
  while [ "${i}" -lt "${LIGHTPANDA_POOL_SIZE_NORMALIZED}" ]; do
    port=$((LIGHTPANDA_BASE_PORT + i))
    instance_data_dir="${LIGHTPANDA_DATA_DIR_RESOLVED}/instance-${i}"
    instance_cache_dir="${LIGHTPANDA_CACHE_DIR_RESOLVED}/instance-${i}"
    mkdir -p "${instance_data_dir}" "${instance_cache_dir}" 2>/dev/null || true

    XDG_DATA_HOME="${instance_data_dir}" \
    XDG_CACHE_HOME="${instance_cache_dir}" \
    /opt/lightpanda/lightpanda serve \
      --host "${LIGHTPANDA_HOST_RESOLVED}" \
      --port "${port}" &

    LIGHTPANDA_PIDS="${LIGHTPANDA_PIDS} $!"
    i=$((i + 1))
  done

  sleep "${LIGHTPANDA_BOOT_WAIT_SECONDS:-2}"
fi

/usr/local/bin/browser-mcp \
  -config "${BROWSER_CONFIG_PATH:-/app/configs/config.json}" \
  -transport stdio \
  "$@"
