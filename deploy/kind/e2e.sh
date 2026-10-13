#!/usr/bin/env bash
# End-to-end team isolation check (Phase 2 exit criterion) against the portal
# running in kind: logs in through Keycloak as each demo user, like a
# browser, and checks what the API returns. Uses port-forwards to the kind
# cluster only; ports 8080 and 8180 must match the realm's redirect URIs.
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

require kubectl curl python3

SECRETS_FILE="${STATE_DIR}/secrets.env"
[[ -f "${SECRETS_FILE}" ]] || die "missing ${SECRETS_FILE}; run make dev-up"
PASSWORD="$(grep '^DEMO_USER_PASSWORD=' "${SECRETS_FILE}" | cut -d= -f2)"

WORK="$(mktemp -d)"
PIDS=()
cleanup() {
  for pid in "${PIDS[@]}"; do kill "${pid}" 2>/dev/null || true; done
  rm -rf "${WORK}"
}
trap cleanup EXIT

PORTAL=http://localhost:8080

# forward starts a port-forward unless something already answers on the port
# (e.g. make dev-portal running in another terminal).
forward() {
  local ns="$1" svc="$2" port="$3" path="$4"
  if ! curl -s -o /dev/null "http://localhost:${port}${path}"; then
    k -n "${ns}" port-forward "svc/${svc}" "${port}:8080" >/dev/null 2>&1 &
    PIDS+=("$!")
  fi
  for _ in $(seq 1 30); do
    curl -sf -o /dev/null "http://localhost:${port}${path}" && return 0
    sleep 1
  done
  die "localhost:${port} (${svc}) did not become ready"
}

forward kubeportal kubeportal 8080 /healthz
forward keycloak keycloak 8180 /realms/kubeportal

# login USER: runs the browser flow and leaves the session in ${WORK}/USER.jar.
login() {
  local user="$1" jar="${WORK}/$1.jar" auth_url page action callback status
  auth_url="$(curl -s -c "${jar}" -b "${jar}" -o /dev/null -w '%{redirect_url}' "${PORTAL}/auth/login")"
  page="$(curl -s -c "${jar}" -b "${jar}" "${auth_url}")"
  action="$(printf '%s' "${page}" | grep -o 'action="[^"]*"' | head -1 \
    | sed 's/^action="//; s/"$//; s/&amp;/\&/g')"
  [[ -n "${action}" ]] || die "${user}: Keycloak login form not found"
  callback="$(curl -s -c "${jar}" -b "${jar}" -o /dev/null -w '%{redirect_url}' \
    --data-urlencode "username=${user}" --data-urlencode "password=${PASSWORD}" "${action}")"
  status="$(curl -s -c "${jar}" -b "${jar}" -o /dev/null -w '%{http_code} %{redirect_url}' "${callback}")"
  [[ "${status}" == "303 ${PORTAL}/" ]] || die "${user}: login failed (${status})"
}

api() { curl -s -b "${WORK}/$1.jar" "${PORTAL}$2"; }
status_of() { curl -s -o /dev/null -w '%{http_code}' -b "${WORK}/$1.jar" "${PORTAL}$2"; }

fail=0
check() {
  local what="$1" got="$2" want="$3"
  if [[ "${got}" == "${want}" ]]; then
    printf 'ok    %s\n' "${what}"
  else
    printf 'FAIL  %s: got %s, want %s\n' "${what}" "${got}" "${want}"
    fail=1
  fi
}

# Names of the services a user sees, sorted and unique.
service_names() {
  api "$1" /api/services | python3 -c \
    'import json,sys; print(",".join(sorted({s["name"] for s in json.load(sys.stdin)["services"]})))'
}

for user in alice bob carol admin; do login "${user}"; done
log "logged in as alice, bob, carol and admin"

check "alice sees only payments" "$(service_names alice)" "checkout-web,payments-api"
check "bob sees only catalog" "$(service_names bob)" "catalog-api,catalog-worker"
check "carol sees nothing" "$(service_names carol)" ""
check "admin sees every workload, including unlabelled" "$(service_names admin)" \
  "catalog-api,catalog-worker,checkout-web,legacy-batch,payments-api"

for suffix in "" /metrics /logs; do
  check "alice gets 404 for catalog-api${suffix}" \
    "$(status_of alice "/api/services/prod/deployment/catalog-api${suffix}")" 404
  check "bob gets 404 for payments-api${suffix}" \
    "$(status_of bob "/api/services/prod/deployment/payments-api${suffix}")" 404
  check "alice gets 404 for legacy-batch${suffix}" \
    "$(status_of alice "/api/services/prod/deployment/legacy-batch${suffix}")" 404
  check "alice reads payments-api${suffix}" \
    "$(status_of alice "/api/services/prod/deployment/payments-api${suffix}")" 200
done

check "alice's logs come only from payments-api pods" "$(api alice \
  '/api/services/prod/deployment/payments-api/logs?limit=50' | python3 -c \
  'import json,sys; l=json.load(sys.stdin)["lines"]; print(bool(l) and all(x["pod"].startswith("payments-api-") for x in l))')" True
check "admin reads legacy-batch logs" \
  "$(status_of admin /api/services/prod/deployment/legacy-batch/logs)" 200
check "logout without CSRF header is rejected" \
  "$(curl -s -o /dev/null -w '%{http_code}' -X POST -b "${WORK}/alice.jar" "${PORTAL}/auth/logout")" 403
check "anonymous API access is rejected" "$(curl -s -o /dev/null -w '%{http_code}' "${PORTAL}/api/services")" 401

exit "${fail}"
