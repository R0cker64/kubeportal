#!/usr/bin/env bash
# Install rehearsal: a second kind cluster (kubeportal-staging) installed the
# way docs/installation.md describes for GKE, before touching the real
# cluster:
#   - Flux reconciles deploy/flux from the GitHub repository (overlays in
#     deploy/rehearsal), including the Keycloak operator and the signed chart
#     published in GHCR (REHEARSAL_VERSION).
#   - A PostgreSQL outside the cluster plays Cloud SQL: reached by IP, TLS
#     only, verified with its CA (sslmode=verify-ca).
#   - Users and groups are created in Keycloak like an admin would.
# Then `make rehearsal-e2e` runs the isolation e2e against it.
#
# Variables: REHEARSAL_VERSION (chart version, default 0.1.0-rc.1),
# REHEARSAL_REF (git branch Flux reads, default main).
export KIND_CLUSTER_NAME=kubeportal-staging
source "$(dirname "${BASH_SOURCE[0]}")/../kind/lib.sh"

require docker kind kubectl helm openssl curl python3

VERSION="${REHEARSAL_VERSION:-0.1.0-rc.1}"
REF="${REHEARSAL_REF:-main}"
REPO_URL="https://github.com/R0cker64/kubeportal"
FLUX_VERSION="2.9.6"
FLUX_SHA256="9c1fda7e401429531ed1478f67ba06b3edff6513b75da7ee47bde9f1c4d4251c"
PG_IMAGE="postgres:18.6-trixie@sha256:74935e72241653ca55e0414067e6d8763aceb8a810eb51b452253ec3dcfc4336"
CLOUDSQL="${CLUSTER_NAME}-cloudsql"

for port in 8080 8180; do
  if curl -s -o /dev/null --max-time 2 "http://localhost:${port}/"; then
    die "port ${port} is in use (make dev-forward?); stop it first: the rehearsal needs 8080 and 8180"
  fi
done

# 1. Cluster with monitoring and demo apps, without the development stack.
KIND_DEV_STACK=0 "${KIND_DIR}/up.sh"

# 2. Credentials, generated once (.kind-kubeportal-staging/ is git-ignored).
SECRETS_FILE="${STATE_DIR}/secrets.env"
if [[ ! -f "${SECRETS_FILE}" ]]; then
  umask 077
  {
    echo "PG_SUPERUSER_PASSWORD=$(openssl rand -hex 16)"
    echo "KUBEPORTAL_DB_PASSWORD=$(openssl rand -hex 16)"
    echo "KEYCLOAK_DB_PASSWORD=$(openssl rand -hex 16)"
    echo "KEYCLOAK_ADMIN_PASSWORD=$(openssl rand -hex 16)"
    echo "OIDC_CLIENT_SECRET=$(openssl rand -hex 32)"
    echo "SESSION_KEY=$(openssl rand -hex 32)"
    echo "DEMO_USER_PASSWORD=$(openssl rand -hex 8)"
  } > "${SECRETS_FILE}"
fi
# shellcheck disable=SC1090
source "${SECRETS_FILE}"

# 3. "Cloud SQL": PostgreSQL on the kind Docker network, TLS required. Its
# certificate does not name the IP, like Cloud SQL's, so only verify-ca works.
TLS_DIR="${STATE_DIR}/cloudsql-tls"
if [[ ! -f "${TLS_DIR}/ca.crt" ]]; then
  log "creating the CA and server certificate of the fake Cloud SQL"
  mkdir -p "${TLS_DIR}"
  openssl req -x509 -newkey rsa:2048 -nodes -days 30 -subj "/CN=rehearsal-cloudsql-ca" \
    -keyout "${TLS_DIR}/ca.key" -out "${TLS_DIR}/ca.crt" 2>/dev/null
  openssl req -newkey rsa:2048 -nodes -subj "/CN=rehearsal-cloudsql" \
    -keyout "${TLS_DIR}/server.key" -out "${TLS_DIR}/server.csr" 2>/dev/null
  openssl x509 -req -in "${TLS_DIR}/server.csr" -CA "${TLS_DIR}/ca.crt" -CAkey "${TLS_DIR}/ca.key" \
    -CAcreateserial -days 30 -out "${TLS_DIR}/server.crt" 2>/dev/null
  chmod 644 "${TLS_DIR}"/*.crt "${TLS_DIR}/server.key"
fi
if ! docker inspect "${CLOUDSQL}" >/dev/null 2>&1; then
  log "starting the fake Cloud SQL (${CLOUDSQL})"
  # The entrypoint copies the key so that it belongs to the postgres user,
  # and pg_hba accepts only TLS connections from the network.
  docker run -d --name "${CLOUDSQL}" --network kind --restart unless-stopped \
    -e POSTGRES_PASSWORD="${PG_SUPERUSER_PASSWORD}" \
    -v "${TLS_DIR}:/tls:ro" \
    --entrypoint sh "${PG_IMAGE}" -c '
      install -d -o postgres -m 700 /var/lib/postgresql/tls
      install -o postgres -m 600 /tls/server.key /tls/server.crt /var/lib/postgresql/tls/
      printf "local all all trust\nhostssl all all 0.0.0.0/0 scram-sha-256\n" > /var/lib/postgresql/pg_hba.conf
      chown postgres /var/lib/postgresql/pg_hba.conf
      exec docker-entrypoint.sh postgres -c ssl=on \
        -c ssl_cert_file=/var/lib/postgresql/tls/server.crt \
        -c ssl_key_file=/var/lib/postgresql/tls/server.key \
        -c hba_file=/var/lib/postgresql/pg_hba.conf' >/dev/null
fi
for _ in $(seq 1 60); do
  docker exec "${CLOUDSQL}" pg_isready -U postgres >/dev/null 2>&1 && break
  sleep 1
done
CLOUDSQL_IP="$(docker inspect -f '{{(index .NetworkSettings.Networks "kind").IPAddress}}' "${CLOUDSQL}")"
APISERVER_IP="$(docker inspect -f '{{(index .NetworkSettings.Networks "kind").IPAddress}}' "${CLUSTER_NAME}-control-plane")"
[[ -n "${CLOUDSQL_IP}" && -n "${APISERVER_IP}" ]] || die "could not read the container IPs"

log "databases and users (docs/installation.md, step 2)"
docker exec -i "${CLOUDSQL}" psql -U postgres -v ON_ERROR_STOP=1 -q <<SQL
SELECT 'CREATE ROLE kubeportal LOGIN' WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'kubeportal')\gexec
SELECT 'CREATE ROLE keycloak LOGIN' WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'keycloak')\gexec
ALTER ROLE kubeportal PASSWORD '${KUBEPORTAL_DB_PASSWORD}';
ALTER ROLE keycloak PASSWORD '${KEYCLOAK_DB_PASSWORD}';
SELECT 'CREATE DATABASE kubeportal OWNER kubeportal' WHERE NOT EXISTS (SELECT 1 FROM pg_database WHERE datname = 'kubeportal')\gexec
SELECT 'CREATE DATABASE keycloak OWNER keycloak' WHERE NOT EXISTS (SELECT 1 FROM pg_database WHERE datname = 'keycloak')\gexec
SQL

# 4. Namespaces and Secrets (docs/installation.md, step 3). stack.yaml also
# declares the namespaces; Flux adopts them.
log "secrets"
for ns in keycloak kubeportal; do
  k create namespace "${ns}" --dry-run=client -o yaml | k apply -f - >/dev/null
done
secret() {
  local ns="$1" name="$2"; shift 2
  k -n "${ns}" create secret generic "${name}" "$@" --dry-run=client -o yaml | k apply -f - >/dev/null
}
secret keycloak keycloak-db --from-literal=username=keycloak --from-literal=password="${KEYCLOAK_DB_PASSWORD}"
secret keycloak cloudsql-ca --from-file=ca.crt="${TLS_DIR}/ca.crt"
secret keycloak keycloak-bootstrap-admin --from-literal=username=bootstrap-admin \
  --from-literal=password="${KEYCLOAK_ADMIN_PASSWORD}"
secret keycloak kubeportal-oidc --from-literal=client-secret="${OIDC_CLIENT_SECRET}"
secret kubeportal kubeportal-oidc --from-literal=client-secret="${OIDC_CLIENT_SECRET}"
secret kubeportal kubeportal-session --from-literal=key="${SESSION_KEY}"
secret kubeportal kubeportal-db-ca --from-file=ca.crt="${TLS_DIR}/ca.crt"
secret kubeportal kubeportal-db --from-literal=url="postgres://kubeportal:${KUBEPORTAL_DB_PASSWORD}@${CLOUDSQL_IP}:5432/kubeportal?sslmode=verify-ca&sslrootcert=/etc/kubeportal/db-ca/ca.crt"

# 5. Flux, pinned and checksum-verified.
FLUX_MANIFEST="${STATE_DIR}/flux-install-${FLUX_VERSION}.yaml"
if [[ ! -f "${FLUX_MANIFEST}" ]]; then
  log "downloading Flux ${FLUX_VERSION}"
  curl -sSfL -o "${FLUX_MANIFEST}.tmp" \
    "https://github.com/fluxcd/flux2/releases/download/v${FLUX_VERSION}/install.yaml"
  echo "${FLUX_SHA256}  ${FLUX_MANIFEST}.tmp" | sha256sum -c - >/dev/null || die "Flux manifest checksum mismatch"
  mv "${FLUX_MANIFEST}.tmp" "${FLUX_MANIFEST}"
fi
log "Flux ${FLUX_VERSION}"
k apply -f "${FLUX_MANIFEST}" >/dev/null
k -n flux-system wait --for=condition=Available deployment --all --timeout=5m >/dev/null

log "Flux reads ${REPO_URL} (${REF}), chart ${VERSION}"
k -n flux-system create configmap rehearsal-vars \
  --from-literal=CLOUDSQL_IP="${CLOUDSQL_IP}" \
  --from-literal=APISERVER_IP="${APISERVER_IP}" \
  --from-literal=KUBEPORTAL_VERSION="${VERSION}" \
  --dry-run=client -o yaml | k apply -f - >/dev/null
k apply -f - >/dev/null <<YAML
apiVersion: source.toolkit.fluxcd.io/v1
kind: GitRepository
metadata:
  name: flux-system
  namespace: flux-system
spec:
  interval: 1m
  url: ${REPO_URL}
  ref:
    branch: ${REF}
---
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: rehearsal
  namespace: flux-system
spec:
  interval: 10m
  sourceRef:
    kind: GitRepository
    name: flux-system
  path: ./deploy/rehearsal/cluster
  prune: true
YAML

log "waiting for keycloak-operator -> keycloak -> kubeportal (up to 20 min)"
for ks in rehearsal keycloak-operator keycloak kubeportal; do
  for _ in $(seq 1 60); do k -n flux-system get kustomization "${ks}" >/dev/null 2>&1 && break; sleep 5; done
  if ! k -n flux-system wait --for=condition=Ready "kustomization/${ks}" --timeout=15m >/dev/null; then
    k -n flux-system get gitrepository,kustomization
    k -n kubeportal get ocirepository,helmrelease 2>/dev/null || true
    die "Flux Kustomization ${ks} is not ready"
  fi
  log "  ${ks} ready"
done

# 6. Users and groups, as an admin would create them in the Keycloak console.
log "Keycloak users and groups"
PIDS=()
trap 'for pid in "${PIDS[@]}"; do kill "${pid}" 2>/dev/null || true; done' EXIT
k_background -n keycloak port-forward svc/keycloak-service 18180:8080 >/dev/null 2>&1
for _ in $(seq 1 30); do curl -s -o /dev/null http://localhost:18180/realms/master && break; sleep 1; done
python3 "${ROOT_DIR}/deploy/rehearsal/keycloak_users.py" http://localhost:18180 \
  bootstrap-admin "${KEYCLOAK_ADMIN_PASSWORD}" "${DEMO_USER_PASSWORD}"

# 7. Demo teams, as an admin would create them in the portal (search is left
# for the e2e, which creates it through the admin API).
docker exec -i "${CLOUDSQL}" psql -U postgres -d kubeportal -v ON_ERROR_STOP=1 -q \
  < "${KIND_DIR}/manifests/seed-teams.sql"

cat <<MSG

Rehearsal ready (cluster ${CLUSTER_NAME}, chart ${VERSION} from GHCR).
  kubeconfig:   ${KUBECONFIG}
  credentials:  ${SECRETS_FILE}
  e2e:          make rehearsal-e2e
  browser:      make rehearsal-forward  -> http://localhost:8080
MSG
