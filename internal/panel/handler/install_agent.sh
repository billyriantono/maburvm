#!/usr/bin/env bash
# MaburVM node agent bootstrap installer.
#
# Usage (copy from the panel's "Add Node" screen):
#   curl -fsSL https://<panel>/install-agent.sh | sudo TOKEN=<node-token> bash
#
# It installs the libvirt RUNTIME (no libvirt-dev), drops the prebuilt agent
# binary + a systemd unit wired with the node token, and starts it. The agent
# listens on this host; the panel connects to it using the node's IP + port.
set -euo pipefail

PANEL_URL="${PANEL_URL:-__PANEL_URL__}"
GRPC_PORT="${AGENT_GRPC_PORT:-50051}"
BIND="${AGENT_BIND_ADDRESS:-0.0.0.0}"
INSTALL_DIR=/opt/maburvm

if [ "${TOKEN:-}" = "" ]; then
  echo "ERROR: TOKEN is required.  Run: curl -fsSL ${PANEL_URL}/install-agent.sh | sudo TOKEN=<node-token> bash" >&2
  exit 1
fi
if [ "$(id -u)" != "0" ]; then
  echo "ERROR: run as root (use sudo)." >&2
  exit 1
fi

case "$(uname -m)" in
  x86_64|amd64) GOARCH=amd64 ;;
  aarch64|arm64) GOARCH=arm64 ;;
  *) echo "ERROR: unsupported architecture $(uname -m)" >&2; exit 1 ;;
esac

echo "==> Installing libvirt runtime + qemu + genisoimage (no libvirt-dev needed)..."
if command -v apt-get >/dev/null 2>&1; then
  export DEBIAN_FRONTEND=noninteractive
  apt-get update -y
  apt-get install -y --no-install-recommends libvirt-daemon-system libvirt-clients qemu-kvm genisoimage curl ca-certificates
elif command -v dnf >/dev/null 2>&1; then
  dnf install -y libvirt qemu-kvm genisoimage curl ca-certificates
elif command -v yum >/dev/null 2>&1; then
  yum install -y libvirt qemu-kvm genisoimage curl ca-certificates
else
  echo "WARN: unknown package manager — ensure libvirt + qemu-kvm + genisoimage are installed." >&2
fi

# Start the libvirt daemon (modular virtqemud on newer distros, monolithic libvirtd otherwise).
systemctl enable --now libvirtd 2>/dev/null || systemctl enable --now virtqemud 2>/dev/null || true

echo "==> Fetching agent binary from ${PANEL_URL} ..."
install -d "$INSTALL_DIR"
curl -fsSL "${PANEL_URL}/api/v1/nodes/agent-binary?arch=${GOARCH}" -o "${INSTALL_DIR}/agent.new"
chmod +x "${INSTALL_DIR}/agent.new"
mv -f "${INSTALL_DIR}/agent.new" "${INSTALL_DIR}/agent"

echo "==> Writing systemd unit..."
cat >/etc/systemd/system/maburvm-agent.service <<UNIT
[Unit]
Description=MaburVM Node Agent
After=network-online.target libvirtd.service virtqemud.service
Wants=network-online.target

[Service]
Environment=AGENT_AUTH_TOKEN=${TOKEN}
Environment=AGENT_BIND_ADDRESS=${BIND}
Environment=AGENT_GRPC_PORT=${GRPC_PORT}
Environment=ENVIRONMENT=development
ExecStart=${INSTALL_DIR}/agent
Restart=always
RestartSec=5
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
UNIT

systemctl daemon-reload
systemctl enable --now maburvm-agent

echo ""
echo "==> MaburVM agent installed and started."
echo "    Listening on ${BIND}:${GRPC_PORT} (this host's public IP must be reachable by the panel)."
echo "    Check status: systemctl status maburvm-agent --no-pager"
echo "    Logs:        journalctl -u maburvm-agent -f"
