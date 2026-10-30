#!/bin/sh
# Delete a random api or worker pod every few seconds. The Service, not the
# pod IP, is what the client talks to, so this should not fail requests.
set -eu
namespace=shop
interval="${1:-4}"
echo "chaos: deleting one api/worker pod every ${interval}s in ${namespace}"
while true; do
  pod=$(kubectl get pods -n "$namespace" -l 'app in (api,worker)' --field-selector=status.phase=Running -o name | shuf -n 1 || true)
  if [ -n "${pod:-}" ]; then
    echo "$(date -u +%H:%M:%S) delete $pod"
    kubectl delete -n "$namespace" "$pod" --wait=false >/dev/null
  fi
  sleep "$interval"
done
