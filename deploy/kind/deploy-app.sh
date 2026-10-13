#!/usr/bin/env bash
# Builds the KubePortal image, loads it into kind and installs the chart.
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

require docker kind helm

cluster_exists || die "kind cluster ${CLUSTER_NAME} not found; run make dev-up first"

IMAGE="kubeportal:dev"
log "building ${IMAGE}"
docker build -t "${IMAGE}" "${ROOT_DIR}"

log "loading ${IMAGE} into kind"
kind load docker-image "${IMAGE}" --name "${CLUSTER_NAME}"

log "installing chart"
h upgrade --install kubeportal "${ROOT_DIR}/deploy/helm/kubeportal" -n kubeportal \
  --set image.repository=kubeportal --set image.tag=dev --set image.pullPolicy=Never \
  --wait --timeout 5m
k -n kubeportal rollout restart deployment/kubeportal
k -n kubeportal rollout status deployment/kubeportal --timeout 4m

# The backend creates the schema on start; then load the demo team mapping.
log "seeding demo teams"
k -n kubeportal exec -i statefulset/postgres -- \
  psql -U kubeportal -d kubeportal -v ON_ERROR_STOP=1 -q < "${KIND_DIR}/manifests/seed-teams.sql"

h test kubeportal -n kubeportal
