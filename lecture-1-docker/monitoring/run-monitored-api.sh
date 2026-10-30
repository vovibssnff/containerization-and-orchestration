#!/usr/bin/env bash
set -Eeuo pipefail

ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
API="$ROOT/api/api"
CG_PARENT="/sys/fs/cgroup/user.slice/user-$(id -u).slice/user@$(id -u).service"
CG="$CG_PARENT/lab1-monitor"
API_PID=""

cleanup() {
    trap - EXIT INT TERM
    if [[ -d "$CG" ]]; then
        printf '1\n' >"$CG/cgroup.kill" 2>/dev/null || true
        rmdir "$CG" 2>/dev/null || true
    elif [[ -n "$API_PID" ]]; then
        kill "$API_PID" 2>/dev/null || true
    fi
}
trap cleanup EXIT INT TERM

if [[ ! -x "$API" || "$ROOT/api/main.go" -nt "$API" ]]; then
    (cd "$ROOT/api" && CGO_ENABLED=0 go build -o api .)
fi

if [[ -d "$CG" ]]; then
    printf '1\n' >"$CG/cgroup.kill" 2>/dev/null || true
    rmdir "$CG"
fi

mkdir "$CG"
printf '134217728\n' >"$CG/memory.max"
printf '0\n' >"$CG/memory.swap.max"
printf '50000 100000\n' >"$CG/cpu.max"
printf '64\n' >"$CG/pids.max"

ADDR=0.0.0.0:8080 "$API" &
API_PID=$!
printf '%s\n' "$API_PID" >"$CG/cgroup.procs"

echo "monitored api pid=$API_PID"
echo "cgroup=$CG"
echo "limits: memory=128MiB swap=0 cpu=0.5 pids=64"
echo "press Ctrl+C to stop and remove the cgroup"

status=0
wait "$API_PID" || status=$?
API_PID=""

echo "api exited with status $status"
cat "$CG/memory.events"
echo "cgroup is being kept alive for Prometheus; press Ctrl+C after the dashboard screenshot"

while true; do
    sleep 3600
done
