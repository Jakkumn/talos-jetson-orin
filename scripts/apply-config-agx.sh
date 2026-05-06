#!/usr/bin/env bash
# apply-config-agx.sh — Apply the AGX test config ONLY to 10.0.10.43.
#
# This script never touches the existing controlplane.yaml / talosconfig.
# Use this for the AGX test node only.
set -euo pipefail
source "$(dirname "$0")/common.sh"

NODE_IP="${NODE_IP:-10.0.10.43}"
CONFIG="${CONFIG:-${REPO_ROOT}/manifests/talos/controlplane-agx.yaml}"
TALOSCONFIG_PATH="${TALOSCONFIG_PATH:-${DIST_DIR}/agx/talosconfig}"
INSECURE="${1:-}"

check_talosctl
[[ -f "${CONFIG}" ]] || error "AGX config not found: ${CONFIG}. Run ./scripts/gen-config-agx.sh first."

CDI_PATCH="${REPO_ROOT}/manifests/talos/machine-patch-cdi.yaml"
[[ -f "${CDI_PATCH}" ]] || error "CDI patch not found: ${CDI_PATCH}"

if [[ "${INSECURE}" == "--insecure" ]]; then
  info "Applying AGX config in MAINTENANCE MODE to ${NODE_IP}"
  talosctl apply-config \
    --nodes "${NODE_IP}" --endpoints "${NODE_IP}" --insecure \
    --file "${CONFIG}" \
    --config-patch "@${CDI_PATCH}"
else
  [[ -f "${TALOSCONFIG_PATH}" ]] || error "AGX talosconfig not found: ${TALOSCONFIG_PATH}"
  info "Applying AGX config to running node ${NODE_IP} (mode: reboot)"
  talosctl apply-config \
    --talosconfig "${TALOSCONFIG_PATH}" \
    --nodes "${NODE_IP}" --endpoints "${NODE_IP}" \
    --file "${CONFIG}" --mode=reboot \
    --config-patch "@${CDI_PATCH}"
fi

info "Waiting for node ${NODE_IP} to return..."
DEADLINE=$(( $(date +%s) + 1200 ))
until talosctl version --nodes "${NODE_IP}" --endpoints "${NODE_IP}" ${INSECURE:+--insecure} &>/dev/null; do
  if (( $(date +%s) > DEADLINE )); then
    error "Node ${NODE_IP} did not return within 20 minutes"
  fi
  printf "."
  sleep 5
done
echo
info "Node is back online."
