#!/usr/bin/env bash
# Local QA lab: SR Linux (containerlab) + network-operator (kind + Tilt), all in WSL.
#
# Run inside the `Ubuntu` WSL distribution (Ubuntu 24.04) as the default user, for example from
# Windows:
#   wsl -d Ubuntu -- bash /mnt/w/Projects/Go/Canopy/lab/operator/lab.sh up
#
# Subcommands:
#   up      deploy SR Linux, clone network-operator if needed, create the kind cluster
#           and check it can reach the lab (routed, not attached)
#   tilt    run Tilt for network-operator in the foreground (UI: http://localhost:10350)
#   device  create the CA secret and Device leaf1 (the SR Linux node) that all Tilt
#           samples target (after Tilt installed the CRDs)
#   status  show lab, cluster and Device state
#   down    delete the kind cluster and destroy the SR Linux lab
set -euo pipefail

CANOPY_LAB="$(cd -- "$(dirname -- "$0")/.." && pwd)"
OPERATOR_DIR="${OPERATOR_DIR:-$HOME/network-operator}"
OPERATOR_SRC="${OPERATOR_SRC:-/mnt/w/Projects/Go/network-operator}"
CLAB_LABDIR_BASE="${CLAB_LABDIR_BASE:-$HOME/clab}"
CLUSTER=network-operator
CTX="kind-$CLUSTER"
NODE="$CLUSTER-control-plane"
LAB_TLS="$CLAB_LABDIR_BASE/clab-canopy-dev/.tls"

export CLAB_LABDIR_BASE
export PATH="$OPERATOR_DIR/bin:/usr/local/go/bin:$HOME/go/bin:$PATH"

lab_up() {
  mkdir -p "$CLAB_LABDIR_BASE"
  if docker ps --format '{{.Names}}' | grep -qx clab-canopy-dev-srl1; then
    echo "SR Linux lab already running"
  else
    # After a WSL restart the lab containers exist but are stopped; redeploy cleanly.
    # This regenerates the lab CA, so run 'device' again afterwards.
    (cd "$CANOPY_LAB" && containerlab deploy -t canopy-dev.clab.yml --reconfigure)
  fi
}

operator_clone() {
  if [ ! -d "$OPERATOR_DIR/.git" ]; then
    git -c core.autocrlf=false clone "$OPERATOR_SRC" "$OPERATOR_DIR"
    git -C "$OPERATOR_DIR" remote set-url origin https://github.com/ironcore-dev/network-operator.git
  fi
}

cluster_up() {
  (cd "$OPERATOR_DIR" && make kind-create kustomize controller-gen)
  # The kind node is NOT attached to the clab network: containerlab allows routed
  # access to its management network, and attaching the node makes kubelet pick the
  # wrong node address after a restart.
  if docker inspect -f '{{json .NetworkSettings.Networks}}' "$NODE" | grep -q '"clab"'; then
    docker network disconnect clab "$NODE"
    docker restart "$NODE" >/dev/null
    kubectl --context "$CTX" wait node/"$NODE" --for=condition=Ready --timeout=180s
  fi
  docker exec "$NODE" bash -c 'timeout 5 bash -c "</dev/tcp/172.20.20.2/57400"' \
    && echo "kind node reaches srl1:57400 (routed)"
}

case "${1:-}" in
  up)
    lab_up
    operator_clone
    cluster_up
    echo "Next: '$0 tilt' (keep it running), then '$0 device'."
    ;;
  tilt)
    cd "$OPERATOR_DIR"
    exec tilt up --context "$CTX" --stream=true --legacy=false
    ;;
  device)
    kubectl --context "$CTX" create secret generic lab-ca \
      --from-file=ca.crt="$LAB_TLS/ca/ca.pem" --dry-run=client -o yaml \
      | kubectl --context "$CTX" apply -f -
    kubectl --context "$CTX" apply -f "$CANOPY_LAB/operator/leaf1-device.yaml"
    ;;
  status)
    containerlab inspect -t "$CANOPY_LAB/canopy-dev.clab.yml" || true
    kubectl --context "$CTX" get pods -n network-operator-system || true
    kubectl --context "$CTX" get devices -A -o wide || true
    ;;
  down)
    (cd "$OPERATOR_DIR" && make kind-delete) || true
    (cd "$CANOPY_LAB" && containerlab destroy -t canopy-dev.clab.yml --cleanup) || true
    ;;
  *)
    sed -n '2,17p' "$0"
    exit 1
    ;;
esac
