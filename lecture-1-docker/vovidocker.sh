#!/usr/bin/env bash
set -Eeuo pipefail

ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
API="$ROOT/api/api"
PROFILE="$ROOT/seccomp.profile"
CG_PARENT="/sys/fs/cgroup/user.slice/user-$(id -u).slice/user@$(id -u).service"
CG="$CG_PARENT/lab1-vovidocker"
RUNNER_PID=""
HEALTH_FILE="/tmp/vovidocker-health.$$"

if [[ "${1:-}" == "__child" ]]; then
    ip link set lo up
    python3 -c 'import socket; socket.sethostname("vovidocker")'
    exec setpriv \
        --bounding-set=-all \
        --inh-caps=-all \
        --ambient-caps=-all \
        --no-new-privs \
        --seccomp-filter "$2" \
        "$3"
fi

MEMORY_MAX="${MEMORY_MAX:-134217728}"
CPU_MAX="${CPU_MAX:-50000 100000}"
PIDS_MAX="${PIDS_MAX:-64}"

for command in unshare nsenter setpriv ip curl python3; do
    command -v "$command" >/dev/null || {
        echo "missing required command: $command" >&2
        exit 1
    }
done

if [[ ! -x "$API" || "$ROOT/api/main.go" -nt "$API" ]]; then
    echo "building api"
    (cd "$ROOT/api" && CGO_ENABLED=0 go build -o api .)
fi

[[ -r "$PROFILE" ]] || {
    echo "missing seccomp profile: $PROFILE" >&2
    exit 1
}

cleanup() {
    trap - EXIT INT TERM
    rm -f "$HEALTH_FILE"
    if [[ -d "$CG" ]]; then
        printf '1\n' >"$CG/cgroup.kill" 2>/dev/null || true
    elif [[ -n "$RUNNER_PID" ]]; then
        kill "$RUNNER_PID" 2>/dev/null || true
    fi
    [[ -z "$RUNNER_PID" ]] || wait "$RUNNER_PID" 2>/dev/null || true
    rmdir "$CG" 2>/dev/null || true
}
trap cleanup EXIT INT TERM

if [[ -d "$CG" ]]; then
    printf '1\n' >"$CG/cgroup.kill" 2>/dev/null || true
    rmdir "$CG" 2>/dev/null || {
        echo "cannot clean existing cgroup: $CG" >&2
        exit 1
    }
fi

mkdir "$CG"
printf '%s\n' "$MEMORY_MAX" >"$CG/memory.max"
printf '0\n' >"$CG/memory.swap.max"
printf '%s\n' "$CPU_MAX" >"$CG/cpu.max"
printf '%s\n' "$PIDS_MAX" >"$CG/pids.max"

# Stop before exec so the parent can move this process into the cgroup.
# Every process subsequently forked by unshare inherits that membership.
bash -c '
    kill -STOP "$$"
    exec unshare \
        --user --map-root-user \
        --pid --fork --mount-proc \
        --mount --uts --ipc --net \
        "$1" __child "$2" "$3"
' _ "$ROOT/vovidocker.sh" "$PROFILE" "$API" &
RUNNER_PID=$!

printf '%s\n' "$RUNNER_PID" >"$CG/cgroup.procs"
kill -CONT "$RUNNER_PID"

for _ in {1..50}; do
    if ! kill -0 "$RUNNER_PID" 2>/dev/null; then
        wait "$RUNNER_PID"
        exit 1
    fi
    if nsenter -t "$RUNNER_PID" -U -n --preserve-credentials \
        curl --silent --fail --max-time 1 http://127.0.0.1:8080/health \
        >"$HEALTH_FILE" 2>/dev/null; then
        break
    fi
    sleep 0.1
done

if [[ ! -s "$HEALTH_FILE" ]]; then
    echo "api did not become healthy" >&2
    exit 1
fi

API_PID="$(pgrep -P "$RUNNER_PID" -n api || true)"
echo "vovidocker is running"
echo "runner host PID: $RUNNER_PID"
echo "api host PID: ${API_PID:-unknown}"
echo "cgroup: $CG"
echo "limits: memory=$MEMORY_MAX swap=0 cpu=$CPU_MAX pids=$PIDS_MAX"
printf 'health from inside net namespace: '
cat "$HEALTH_FILE"
rm -f "$HEALTH_FILE"
echo "press Ctrl+C to stop"

wait "$RUNNER_PID"
