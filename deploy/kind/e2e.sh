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
# Other clusters (the install rehearsal) never reuse a listener: it could be
# forwarding to the development cluster.
forward() {
  local ns="$1" svc="$2" port="$3" path="$4"
  if curl -s -o /dev/null "http://localhost:${port}${path}"; then
    [[ "${CLUSTER_NAME}" == kubeportal ]] || die "port ${port} is already in use; stop other port-forwards"
  else
    k_background -n "${ns}" port-forward "svc/${svc}" "${port}:8080" >/dev/null 2>&1
  fi
  for _ in $(seq 1 30); do
    curl -sf -o /dev/null "http://localhost:${port}${path}" && return 0
    sleep 1
  done
  die "localhost:${port} (${svc}) did not become ready"
}

forward kubeportal kubeportal 8080 /healthz
# The operator-managed Keycloak Service is keycloak-service.
forward keycloak "${KEYCLOAK_SERVICE:-keycloak}" 8180 /realms/kubeportal

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
# admin_call METHOD PATH [JSON]: state-changing admin request with the CSRF
# header and Origin a browser would send; prints the HTTP status.
admin_call() {
  local args=(-s -o /dev/null -w '%{http_code}' -X "$1" -b "${WORK}/admin.jar"
    -H 'X-Requested-With: kubeportal' -H "Origin: ${PORTAL}")
  [[ $# -ge 3 ]] && args+=(-H 'Content-Type: application/json' --data "$3")
  curl "${args[@]}" "${PORTAL}$2"
}
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

for user in alice bob carol dave admin; do login "${user}"; done
log "logged in as alice, bob, carol, dave and admin"

# Team `search` is created below; start from a clean state when re-run.
admin_call DELETE /api/admin/teams/search >/dev/null

check "alice sees only payments" "$(service_names alice)" "checkout-web,payments-api"
check "bob sees only catalog" "$(service_names bob)" "catalog-api,catalog-worker"
check "carol sees nothing" "$(service_names carol)" ""
check "dave sees nothing while team search does not exist" "$(service_names dave)" ""
check "admin sees every workload, including unlabelled" "$(service_names admin)" \
  "catalog-api,catalog-worker,checkout-web,legacy-batch,payments-api,search-api"

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
log "Phase 3: an admin creates a team and its members see their apps"
for user in alice dave; do
  check "${user} cannot list teams" "$(status_of "${user}" /api/admin/teams)" 403
  check "${user} cannot list nodes" "$(status_of "${user}" /api/admin/nodes)" 403
done
check "admin lists nodes" "$(api admin /api/admin/nodes | python3 -c \
  'import json,sys; d=json.load(sys.stdin); print(len(d["nodes"]) > 0 and all(n["ready"] for n in d["nodes"]))')" True
check "search-api is listed with an unregistered team" "$(api admin /api/admin/workloads/unassigned | python3 -c \
  'import json,sys; print(",".join(sorted({s["name"] for s in json.load(sys.stdin)["unknownTeam"]})))')" search-api
check "team creation without CSRF header is rejected" "$(curl -s -o /dev/null -w '%{http_code}' -X POST \
  -b "${WORK}/admin.jar" -H 'Content-Type: application/json' \
  --data '{"name":"search","displayName":"Search","groups":["team-search"]}' "${PORTAL}/api/admin/teams")" 403
check "admin creates team search" "$(admin_call POST /api/admin/teams \
  '{"name":"search","displayName":"Search","groups":["team-search"]}')" 201
check "dave now sees search-api" "$(service_names dave)" "search-api"
check "dave reads search-api logs" "$(status_of dave /api/services/prod/deployment/search-api/logs)" 200
check "alice still does not see search-api" "$(status_of alice /api/services/prod/deployment/search-api)" 404
check "admin removes the group from team search" "$(admin_call PUT /api/admin/teams/search \
  '{"displayName":"Search","groups":[]}')" 200
check "dave loses access immediately" "$(service_names dave)" ""
check "admin deletes team search" "$(admin_call DELETE /api/admin/teams/search)" 204

check "logout without CSRF header is rejected" \
  "$(curl -s -o /dev/null -w '%{http_code}' -X POST -b "${WORK}/alice.jar" "${PORTAL}/auth/logout")" 403
# Deep links must render when loaded directly (CSP base-uri vs <base href>).
CHROME="$(command -v google-chrome || command -v chromium || command -v chromium-browser || true)"
if [[ -n "${CHROME}" ]]; then
  for path in /services/prod/deployment/payments-api /admin/nodes; do
    check "deep link ${path} renders" "$(timeout 60 "${CHROME}" --headless=new --disable-gpu \
      --virtual-time-budget=8000 --dump-dom "${PORTAL}${path}" 2>/dev/null | grep -c 'Iniciar sesión')" 1
  done
else
  log "no Chrome found: skipping the deep link check"
fi
check "anonymous API access is rejected" "$(curl -s -o /dev/null -w '%{http_code}' "${PORTAL}/api/services")" 401

exit "${fail}"
