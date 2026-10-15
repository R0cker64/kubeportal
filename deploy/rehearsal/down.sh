#!/usr/bin/env bash
# Deletes the install rehearsal: the kubeportal-staging kind cluster, the fake
# Cloud SQL container and their local state (credentials, certificates).
export KIND_CLUSTER_NAME=kubeportal-staging
source "$(dirname "${BASH_SOURCE[0]}")/../kind/lib.sh"

require docker kind

if cluster_exists; then
  log "deleting kind cluster ${CLUSTER_NAME}"
  kind delete cluster --name "${CLUSTER_NAME}"
fi
docker rm -f "${CLUSTER_NAME}-cloudsql" >/dev/null 2>&1 || true
[[ "${STATE_DIR}" == "${ROOT_DIR}/.kind-${CLUSTER_NAME}" ]] && rm -rf "${STATE_DIR}"
log "rehearsal removed"
