#!/usr/bin/env bash
# Local lab: SR Linux (containerlab) + network-operator (kind + Tilt), in WSL or Linux.
#
# Run from the repository root. On Windows, WSL starts in the current folder:
#   wsl -d Ubuntu -- bash lab/operator/lab.sh up
#
# Subcommands:
#   node      deploy only the SR Linux node (fresh config) and export its CA
#   node-down destroy the SR Linux node
#   up        node + clone network-operator if needed + kind cluster that reaches the
#             lab network (routed, not attached)
#   tilt      run Tilt for network-operator in the foreground (UI: http://localhost:10350)
#   device    create the CA secret and Device leaf1 (the SR Linux node) that all Tilt
#             samples target (after Tilt installed the CRDs)
#   ca        copy the lab CA to lab/.tls/ca.pem for Canopy (node, up and device do it too)
#   kubeconfig write the kind cluster's kubeconfig to lab/.kube/config for Canopy (gitignored)
#   status    show lab, cluster and Device state
#   down      delete the kind cluster and destroy the SR Linux node
#   disable   for a long break: down, remove the lab's container images (SR Linux,
#             kind node, registry, cert-manager, Tilt builds, gnmic; ~3.7 GB) and the
#             kind network, and turn the SSH jump host off, so nothing starts on its
#             own after a restart (needs sudo for the jump host)
#   enable    undo 'disable': jump host on, then 'up' (downloads the images again)
#   jumphost  (as root: wsl -d Ubuntu -u root -- bash lab/operator/lab.sh jumphost)
#             SSH server on 127.0.0.1:2222 with user "canopy" (port forwarding only),
#             authorizing the lab key lab/.ssh/canopy_lab_ed25519 (created if missing),
#             so Canopy can reach the lab network through a jump host
#
# Generated files stay in the repository and are gitignored: lab/.tls, lab/.ssh.
# Settings (environment): OPERATOR_DIR (default ~/network-operator, on the Linux
# filesystem), OPERATOR_SRC (default: a network-operator clone next to this
# repository, else GitHub), CLAB_LABDIR_BASE (default ~/clab).
set -euo pipefail

CANOPY_LAB="$(cd -- "$(dirname -- "$0")/.." && pwd)"
OPERATOR_DIR="${OPERATOR_DIR:-$HOME/network-operator}"
if [ -z "${OPERATOR_SRC:-}" ]; then
  if [ -d "$CANOPY_LAB/../../network-operator/.git" ]; then
    OPERATOR_SRC="$(cd "$CANOPY_LAB/../../network-operator" && pwd)"
  else
    OPERATOR_SRC=https://github.com/ironcore-dev/network-operator.git
  fi
fi
CLAB_LABDIR_BASE="${CLAB_LABDIR_BASE:-$HOME/clab}" # Linux filesystem: SR Linux needs real permissions
CLUSTER=network-operator
CTX="kind-$CLUSTER"
NODE="$CLUSTER-control-plane"
LAB_TLS="$CLAB_LABDIR_BASE/clab-canopy-dev/.tls"
JUMP_USER=canopy
JUMP_KEY="$CANOPY_LAB/.ssh/canopy_lab_ed25519"

export CLAB_LABDIR_BASE
export PATH="$OPERATOR_DIR/bin:/usr/local/go/bin:$HOME/go/bin:$PATH"

node_up() {
  mkdir -p "$CLAB_LABDIR_BASE"
  if docker ps --format '{{.Names}}' | grep -qx clab-canopy-dev-srl1; then
    echo "SR Linux node already running"
  else
    # After a WSL restart the lab containers exist but are stopped; redeploy cleanly.
    # This regenerates the lab CA, so run 'device' again afterwards.
    (cd "$CANOPY_LAB" && containerlab deploy -t canopy-dev.clab.yml --reconfigure)
  fi
  export_ca
}

# export_ca copies the lab CA to lab/.tls/ca.pem, where the Canopy presets in
# lab/connections.yaml expect it.
export_ca() {
  mkdir -p "$CANOPY_LAB/.tls"
  cp "$LAB_TLS/ca/ca.pem" "$CANOPY_LAB/.tls/ca.pem"
  echo "lab CA exported to lab/.tls/ca.pem"
}

# as_root runs a command as root: directly, with sudo, or explains how.
as_root() {
  if [ "$(id -u)" = 0 ]; then
    "$@"
  elif sudo -n true 2>/dev/null || [ -t 0 ]; then
    sudo "$@"
  else
    echo "needs root; run: wsl -d Ubuntu -u root -- $*" >&2
    return 1
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

jumphost() {
  [ "$(id -u)" = 0 ] || { echo "run as root: wsl -d Ubuntu -u root -- bash lab/operator/lab.sh jumphost"; exit 1; }
  command -v sshd >/dev/null || DEBIAN_FRONTEND=noninteractive apt-get install -y -qq openssh-server >/dev/null
  id "$JUMP_USER" >/dev/null 2>&1 || useradd --system --create-home --shell /usr/sbin/nologin "$JUMP_USER"
  cat > /etc/ssh/sshd_config.d/canopy-lab.conf <<EOF
Port 2222
ListenAddress 127.0.0.1
PasswordAuthentication no
Match User $JUMP_USER
    AllowTcpForwarding yes
    PermitTTY no
    X11Forwarding no
EOF
  systemctl disable --now ssh.socket >/dev/null 2>&1 || true
  systemctl enable ssh >/dev/null 2>&1
  systemctl restart ssh
  if [ ! -f "$JUMP_KEY" ]; then
    mkdir -p "$(dirname "$JUMP_KEY")"
    ssh-keygen -q -t ed25519 -N "" -C canopy-lab -f "$JUMP_KEY"
  fi
  home="$(getent passwd "$JUMP_USER" | cut -d: -f6)"
  install -d -m 700 -o "$JUMP_USER" -g "$JUMP_USER" "$home/.ssh"
  tr -d '\r' < "$JUMP_KEY.pub" > "$home/.ssh/authorized_keys"
  chown "$JUMP_USER:$JUMP_USER" "$home/.ssh/authorized_keys"
  chmod 600 "$home/.ssh/authorized_keys"
  echo "SSH jump host: $JUMP_USER@127.0.0.1:2222, key lab/.ssh/canopy_lab_ed25519"
  echo "Host key (trust it on first connect):"
  ssh-keygen -lf /etc/ssh/ssh_host_ecdsa_key.pub
}

case "${1:-}" in
  node)
    node_up
    ;;
  node-down)
    (cd "$CANOPY_LAB" && containerlab destroy -t canopy-dev.clab.yml --cleanup)
    ;;
  up)
    node_up
    operator_clone
    cluster_up
    echo "Next: 'lab.sh tilt' (keep it running), then 'lab.sh device'."
    ;;
  tilt)
    cd "$OPERATOR_DIR"
    exec tilt up --context "$CTX" --stream=true --legacy=false
    ;;
  kubeconfig)
    mkdir -p "$CANOPY_LAB/.kube"
    kind get kubeconfig --name "$CLUSTER" > "$CANOPY_LAB/.kube/config"
    echo "kubeconfig written to lab/.kube/config (context $CTX)"
    ;;
  ca)
    export_ca
    ;;
  device)
    export_ca
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
  disable)
    # For a long break: everything down, images removed, nothing left to start on its own.
    "$0" down
    # only the lab's images, by repository; other Docker images are left alone
    ids="$(docker images -a --format '{{.Repository}} {{.ID}}' \
      | grep -E '^(ghcr\.io/nokia/srlinux|kindest/node|registry|localhost:5000/[^ ]+|quay\.io/jetstack/cert-manager-[^ ]+|ghcr\.io/openconfig/gnmic|busybox) ' \
      | awk '{print $2}' | sort -u)"
    if [ -n "$ids" ]; then
      docker rmi -f $ids >/dev/null
      echo "removed $(echo "$ids" | wc -l) lab image(s)"
    fi
    docker network rm kind >/dev/null 2>&1 || true
    if systemctl is-enabled ssh >/dev/null 2>&1 || systemctl is-active ssh >/dev/null 2>&1; then
      as_root systemctl disable --now ssh ssh.socket && echo "SSH jump host disabled"
    fi
    echo "Lab disabled. 'lab.sh enable' brings it back (downloads the images again)."
    ;;
  enable)
    # Undo 'disable': jump host on, node and cluster up (images are downloaded again).
    as_root bash "$0" jumphost || echo "continuing without the jump host" >&2
    "$0" up
    ;;
  jumphost)
    jumphost
    ;;
  *)
    sed -n '2,33p' "$0"
    exit 1
    ;;
esac
