#!/usr/bin/env bash
# Deletes the local kind cluster. Only ever touches the kind cluster.
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

require kind

if cluster_exists; then
  log "deleting kind cluster ${CLUSTER_NAME}"
  kind delete cluster --name "${CLUSTER_NAME}" --kubeconfig "${KUBECONFIG}"
else
  log "kind cluster ${CLUSTER_NAME} does not exist"
fi
rm -f "${KUBECONFIG}"
