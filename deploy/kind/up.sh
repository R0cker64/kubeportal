#!/usr/bin/env bash
# Creates the local kind cluster with Loki, Alloy, Prometheus, Keycloak,
# PostgreSQL and the demo apps. Idempotent: safe to run again.
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

require docker kind kubectl helm openssl

# kind needs higher inotify limits than most distro defaults, otherwise log
# streaming fails with "too many open files".
if [[ -r /proc/sys/fs/inotify/max_user_instances ]] \
  && (( $(cat /proc/sys/fs/inotify/max_user_instances) < 512 )); then
  printf '\033[1;33mwarning:\033[0m fs.inotify.max_user_instances < 512; log streaming may fail.\n' >&2
  printf '         See docs/development.md ("Límites de inotify").\n' >&2
fi

mkdir -p "${STATE_DIR}"
chmod 700 "${STATE_DIR}"

if cluster_exists; then
  log "kind cluster ${CLUSTER_NAME} already exists"
  kind export kubeconfig --name "${CLUSTER_NAME}" --kubeconfig "${KUBECONFIG}"
else
  log "creating kind cluster ${CLUSTER_NAME}"
  kind create cluster --config "${KIND_DIR}/kind-config.yaml" --kubeconfig "${KUBECONFIG}" --wait 120s
fi
chmod 600 "${KUBECONFIG}"

log "namespaces"
for ns in monitoring keycloak kubeportal dev staging prod; do
  k create namespace "${ns}" --dry-run=client -o yaml | k apply -f - >/dev/null
done
for ns in kubeportal dev staging prod; do
  k label namespace "${ns}" --overwrite \
    pod-security.kubernetes.io/enforce=restricted \
    pod-security.kubernetes.io/warn=restricted >/dev/null
done

# Random local credentials, generated once and kept out of git (.kind/ is ignored).
SECRETS_FILE="${STATE_DIR}/secrets.env"
if [[ ! -f "${SECRETS_FILE}" ]]; then
  log "generating local credentials in ${SECRETS_FILE}"
  umask 077
  {
    echo "KEYCLOAK_ADMIN_PASSWORD=$(openssl rand -hex 16)"
    echo "DEMO_USER_PASSWORD=$(openssl rand -hex 8)"
    echo "OIDC_CLIENT_SECRET=$(openssl rand -hex 32)"
    echo "DB_PASSWORD=$(openssl rand -hex 16)"
    echo "SESSION_KEY=$(openssl rand -hex 32)"
  } > "${SECRETS_FILE}"
fi
# shellcheck disable=SC1090
source "${SECRETS_FILE}"

apply_secret() {
  local ns="$1" name="$2"; shift 2
  k -n "${ns}" create secret generic "${name}" "$@" --dry-run=client -o yaml | k apply -f - >/dev/null
}
apply_secret keycloak keycloak-bootstrap \
  --from-literal=username=admin \
  --from-literal=password="${KEYCLOAK_ADMIN_PASSWORD}" \
  --from-literal=demo-user-password="${DEMO_USER_PASSWORD}"
apply_secret keycloak kubeportal-oidc --from-literal=client-secret="${OIDC_CLIENT_SECRET}"
apply_secret kubeportal kubeportal-oidc --from-literal=client-secret="${OIDC_CLIENT_SECRET}"
apply_secret kubeportal kubeportal-db --from-literal=password="${DB_PASSWORD}" \
  --from-literal=url="postgres://kubeportal:${DB_PASSWORD}@postgres.kubeportal.svc:5432/kubeportal?sslmode=disable"
apply_secret kubeportal kubeportal-session --from-literal=key="${SESSION_KEY}"

log "Loki ${LOKI_CHART_VERSION}"
h upgrade --install loki loki --repo https://grafana.github.io/helm-charts \
  --version "${LOKI_CHART_VERSION}" -n monitoring -f "${KIND_DIR}/values/loki.yaml" --wait --timeout 10m

log "Alloy ${ALLOY_CHART_VERSION}"
h upgrade --install alloy alloy --repo https://grafana.github.io/helm-charts \
  --version "${ALLOY_CHART_VERSION}" -n monitoring -f "${KIND_DIR}/values/alloy.yaml" --wait --timeout 5m

log "Prometheus ${PROMETHEUS_CHART_VERSION}"
h upgrade --install prometheus prometheus --repo https://prometheus-community.github.io/helm-charts \
  --version "${PROMETHEUS_CHART_VERSION}" -n monitoring -f "${KIND_DIR}/values/prometheus.yaml" --wait --timeout 10m

log "Keycloak"
k -n keycloak create configmap keycloak-realm \
  --from-file=kubeportal-realm.json="${KIND_DIR}/manifests/keycloak-realm.json" \
  --dry-run=client -o yaml | k apply -f - >/dev/null
k apply -f "${KIND_DIR}/manifests/keycloak.yaml"

log "PostgreSQL"
k apply -f "${KIND_DIR}/manifests/postgres.yaml"

log "demo apps"
for ns in dev staging prod; do
  k -n "${ns}" apply -f "${KIND_DIR}/manifests/demo-apps.yaml"
done

log "waiting for Keycloak, PostgreSQL and demo apps"
k -n keycloak rollout status deployment/keycloak --timeout 10m
k -n kubeportal rollout status statefulset/postgres --timeout 5m
for ns in dev staging prod; do
  k -n "${ns}" rollout status deployment/payments-api --timeout 5m
done

cat <<MSG

Local environment ready.
  kubeconfig:   ${KUBECONFIG}   (your current kubectl context was not changed)
  credentials:  ${SECRETS_FILE}
  demo users:   admin (kubeportal-admin), alice (team-payments), bob (team-catalog),
                dave (team-search, no team until an admin creates it), carol (no group)
  Keycloak:     make dev-keycloak  -> http://localhost:8180
  Verify:       make dev-verify
MSG
