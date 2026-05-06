#!/usr/bin/env bash
# gen-config-agx.sh — Generate a fresh Talos machine config for the AGX test node.
#
# This creates a standalone AGX control-plane config and writes it to:
#   - manifests/talos/controlplane-agx.yaml   (tracked config)
#   - dist/agx/talosconfig                   (client config, ignored)
#
# It does NOT touch the existing controlplane.yaml / talosconfig used by other
# Jetsons or the current NX setup.
set -euo pipefail
source "$(dirname "$0")/common.sh"

NODE_IP="${NODE_IP:-10.0.10.43}"
CLUSTER_NAME="${CLUSTER_NAME:-jetson-agx-test}"
INSTALL_IMAGE="${INSTALL_IMAGE:-ghcr.io/schwankner/custom-installer:v1.13.0-6.18.24-nvgpu5.10.10}"
INSTALL_DISK="${INSTALL_DISK:-/dev/nvme0n1}"
AGX_DIR="${AGX_DIR:-${DIST_DIR}/agx}"
OUT_CONFIG="${OUT_CONFIG:-${REPO_ROOT}/manifests/talos/controlplane-agx.yaml}"
OUT_TALOSCONFIG="${OUT_TALOSCONFIG:-${AGX_DIR}/talosconfig}"
PATCH_AGX="${REPO_ROOT}/manifests/talos/machine-patch-agx.yaml"
PATCH_CDI="${REPO_ROOT}/manifests/talos/machine-patch-cdi.yaml"

check_talosctl
[[ -f "${PATCH_AGX}" ]] || error "AGX patch not found: ${PATCH_AGX}"
[[ -f "${PATCH_CDI}" ]] || error "CDI patch not found: ${PATCH_CDI}"

mkdir -p "${AGX_DIR}"

info "Generating standalone AGX Talos config"
info "  Cluster:      ${CLUSTER_NAME}"
info "  Node IP:      ${NODE_IP}"
info "  Install disk: ${INSTALL_DISK}"
info "  Install image:${INSTALL_IMAGE}"
info "  Output:       ${OUT_CONFIG}"

TMPDIR=$(mktemp -d)
trap 'rm -rf "${TMPDIR}"' EXIT

talosctl gen config "${CLUSTER_NAME}" "https://${NODE_IP}:6443" \
  --install-disk "${INSTALL_DISK}" \
  --install-image "${INSTALL_IMAGE}" \
  --output-dir "${TMPDIR}" \
  --with-docs=false \
  --force

rm -f "${TMPDIR}/worker.yaml"

# Apply the AGX install patch and CDI enablement.
talosctl machineconfig patch "${TMPDIR}/controlplane.yaml" \
  --patch "@${PATCH_AGX}" \
  --output "${TMPDIR}/controlplane.yaml"

talosctl machineconfig patch "${TMPDIR}/controlplane.yaml" \
  --patch "@${PATCH_CDI}" \
  --output "${TMPDIR}/controlplane.yaml"

# Talos v1.13+ tends to emit grubUseUKICmdline=true in generated ARM64 configs.
# That breaks UKI-based Jetson boots, so strip it out.
python3 - <<'PY' "${TMPDIR}/controlplane.yaml"
from pathlib import Path
p = Path(__import__('sys').argv[1])
text = p.read_text()
text = text.replace('        wipe: true\n        grubUseUKICmdline: true\n', '        wipe: true\n')
p.write_text(text)
PY

cp "${TMPDIR}/controlplane.yaml" "${OUT_CONFIG}"
cp "${TMPDIR}/talosconfig" "${OUT_TALOSCONFIG}"

info "Generated: ${OUT_CONFIG}"
info "Generated: ${OUT_TALOSCONFIG}"
