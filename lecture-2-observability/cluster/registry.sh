#!/usr/bin/env bash
# Local registry + kind cluster, following
# https://kind.sigs.k8s.io/docs/user/local-registry/
set -euo pipefail

reg_name='kind-registry'
reg_port='5001'
cluster_name="${KIND_CLUSTER_NAME:-lab2}"
script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

if [ "$(docker inspect -f '{{.State.Running}}' "${reg_name}" 2>/dev/null || true)" != 'true' ]; then
  docker run -d --restart=unless-stopped \
    -p "127.0.0.1:${reg_port}:5000" \
    --network bridge \
    --name "${reg_name}" \
    registry:3
fi

if ! kind get clusters | grep -qx "${cluster_name}"; then
  kind create cluster --name "${cluster_name}" --config "${script_dir}/kind.yaml"
fi

# From a kind node, localhost:5001 is the node itself. Map that name to the
# registry container on the kind network (kind-registry:5000).
REGISTRY_DIR="/etc/containerd/certs.d/localhost:${reg_port}"
for node in $(kind get nodes --name "${cluster_name}"); do
  docker exec "${node}" mkdir -p "${REGISTRY_DIR}"
  cat <<EOF | docker exec -i "${node}" cp /dev/stdin "${REGISTRY_DIR}/hosts.toml"
[host."http://${reg_name}:5000"]
EOF
done

# {{json .NetworkSettings.Networks.kind}} is the string "null" when the
# container is not attached. index does not print that, so the connect was skipped.
if [ "$(docker inspect -f='{{json .NetworkSettings.Networks.kind}}' "${reg_name}")" = 'null' ]; then
  docker network connect "kind" "${reg_name}"
fi

# Lets tools (and later labs) discover the local registry.
kubectl apply -f - <<EOF
apiVersion: v1
kind: ConfigMap
metadata:
  name: local-registry-hosting
  namespace: kube-public
data:
  localRegistryHosting.v1: |
    host: "localhost:${reg_port}"
    help: "https://kind.sigs.k8s.io/docs/user/local-registry/"
EOF
