#!/usr/bin/env bash
# Read-only preflight for installing KubePortal on a cluster: checks the
# prerequisites in docs/architecture.md and tells which cAdvisor label holds
# the node name. It only runs `kubectl get` and temporary port-forwards to
# Loki and Prometheus; it never changes the cluster.
#
# Usage:
#   deploy/preflight/check-cluster.sh --context <context> [--kubeconfig <file>] \
#     --environments dev,staging,prod \
#     --loki <namespace>/<service>:<port> --prometheus <namespace>/<service>:<port>
#
# The context is mandatory on purpose: the current kubectl context is never used.
set -euo pipefail

usage() { sed -n '2,13p' "$0" | sed 's/^# \{0,1\}//'; exit 2; }
die() { printf '\033[1;31merror:\033[0m %s\n' "$*" >&2; exit 1; }

CONTEXT="" KUBECONFIG_FILE="" ENVIRONMENTS="" LOKI="" PROMETHEUS=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --context) CONTEXT="${2:-}"; shift 2 ;;
    --kubeconfig) KUBECONFIG_FILE="${2:-}"; shift 2 ;;
    --environments) ENVIRONMENTS="${2:-}"; shift 2 ;;
    --loki) LOKI="${2:-}"; shift 2 ;;
    --prometheus) PROMETHEUS="${2:-}"; shift 2 ;;
    -h|--help) usage ;;
    *) die "unknown argument: $1" ;;
  esac
done
[[ -n "${CONTEXT}" && -n "${ENVIRONMENTS}" && -n "${LOKI}" && -n "${PROMETHEUS}" ]] || usage
for bin in kubectl curl python3; do command -v "${bin}" >/dev/null || die "missing ${bin}"; done

svc_re='^([a-z0-9-]+)/([a-z0-9-]+):([0-9]+)$'
[[ "${LOKI}" =~ ${svc_re} ]] || die "--loki must be <namespace>/<service>:<port>"
[[ "${PROMETHEUS}" =~ ${svc_re} ]] || die "--prometheus must be <namespace>/<service>:<port>"

KARGS=(--context "${CONTEXT}")
[[ -n "${KUBECONFIG_FILE}" ]] && KARGS+=(--kubeconfig "${KUBECONFIG_FILE}")
k() { kubectl "${KARGS[@]}" "$@"; }

PIDS=()
cleanup() { for pid in "${PIDS[@]}"; do kill "${pid}" 2>/dev/null || true; done; }
trap cleanup EXIT

fail=0
ok() { printf '\033[32mok\033[0m    %s\n' "$*"; }
warn() { printf '\033[33mwarn\033[0m  %s\n' "$*"; }
bad() { printf '\033[31mFAIL\033[0m  %s\n' "$*"; fail=1; }
section() { printf '\n\033[1;34m==>\033[0m %s\n' "$*"; }

server="$(k config view --minify -o jsonpath='{.clusters[0].cluster.server}')"
section "cluster ${CONTEXT} (${server}), read-only checks"

# --- Workload labels -----------------------------------------------------------
section "team and app.kubernetes.io/name labels in ${ENVIRONMENTS}"
IFS=',' read -r -a envs <<< "${ENVIRONMENTS}"
for ns in "${envs[@]}"; do
  ns="${ns// /}"
  if ! k get namespace "${ns}" >/dev/null 2>&1; then bad "namespace ${ns} does not exist"; continue; fi
  k -n "${ns}" get deployments,statefulsets -o json | python3 -c '
import json, sys
ns = sys.argv[1]
items = json.load(sys.stdin)["items"]
missing_team, missing_tpl, missing_app = [], [], []
for w in items:
    name = w["kind"].lower() + "/" + w["metadata"]["name"]
    labels = w["metadata"].get("labels") or {}
    tpl = w["spec"]["template"]["metadata"].get("labels") or {}
    if "team" not in labels: missing_team.append(name)
    elif tpl.get("team") != labels["team"]: missing_tpl.append(name)
    if "app.kubernetes.io/name" not in tpl: missing_app.append(name)
print(f"{ns}: {len(items)} workloads, {len(items) - len(missing_team)} with team")
for title, lst in (("without team label (admin only)", missing_team),
                   ("team label missing or different in the pod template", missing_tpl),
                   ("pod template without app.kubernetes.io/name (no logs/metrics)", missing_app)):
    if lst: print(f"  {title}: {", ".join(lst[:10])}{" ..." if len(lst) > 10 else ""}")
' "${ns}"
done

# --- Port-forwards ---------------------------------------------------------------
forward() {
  local spec="$1" local_port="$2" path="$3"
  [[ "${spec}" =~ ${svc_re} ]]
  # kubectl directly (not the k function), so $! is kubectl's own PID.
  kubectl "${KARGS[@]}" -n "${BASH_REMATCH[1]}" port-forward "svc/${BASH_REMATCH[2]}" \
    "${local_port}:${BASH_REMATCH[3]}" >/dev/null 2>&1 &
  PIDS+=("$!")
  for _ in $(seq 1 30); do
    curl -s -o /dev/null "http://127.0.0.1:${local_port}${path}" && return 0
    sleep 0.5
  done
  return 1
}
query() { # base path query -> JSON
  curl -sfG --max-time 20 "$1$2" --data-urlencode "query=$3"
}

# --- Loki ------------------------------------------------------------------------
section "Loki (${LOKI})"
LOKI_URL=http://127.0.0.1:23100
if forward "${LOKI}" 23100 /ready; then
  out="$(query "${LOKI_URL}" /loki/api/v1/query 'sum by (team) (count_over_time({team=~".+"}[1h]))' || true)"
  teams="$(printf '%s' "${out}" | python3 -c 'import json,sys
try: r=json.load(sys.stdin)["data"]["result"]
except Exception: r=[]
print(",".join(sorted(x["metric"].get("team","") for x in r)))' )"
  if [[ -n "${teams}" ]]; then ok "logs labelled with team in the last hour: ${teams}"; else bad "no logs with a team label in the last hour (the collector must copy the pod label team)"; fi
  out="$(query "${LOKI_URL}" /loki/api/v1/query 'count(count_over_time({app=~".+", team=~".+"}[1h]))' || true)"
  if printf '%s' "${out}" | grep -q '"result":\[{'; then ok "logs carry the app label"; else bad "no logs with an app label (copy the pod label app.kubernetes.io/name as app)"; fi
  out="$(query "${LOKI_URL}" /loki/api/v1/query 'count(count_over_time({team=~".+"} | detected_level=~".+" [1h]))' || true)"
  if printf '%s' "${out}" | grep -q '"result":\[{'; then ok "detected_level is available (level filter works)"; else warn "no detected_level: the level filter will return nothing (enable discover_log_levels)"; fi
  warn "Loki retention is not checked here: confirm limits_config.retention_period and compactor.retention_enabled (KUBEPORTAL_LOG_MAX_RANGE must not exceed it)"
else
  bad "cannot reach Loki through ${LOKI}"
fi

# --- Prometheus -------------------------------------------------------------------
section "Prometheus (${PROMETHEUS})"
PROM_URL=http://127.0.0.1:29090
if forward "${PROMETHEUS}" 29090 /-/ready; then
  has() { query "${PROM_URL}" /api/v1/query "$1" | grep -q '"result":\[{'; }
  if has 'kube_pod_labels{label_team!=""}'; then ok "kube-state-metrics exposes label_team"; else bad "kube_pod_labels has no label_team (--metric-labels-allowlist=pods=[team,app.kubernetes.io/name])"; fi
  if has 'kube_pod_labels{label_app_kubernetes_io_name!=""}'; then ok "kube-state-metrics exposes label_app_kubernetes_io_name"; else bad "kube_pod_labels has no label_app_kubernetes_io_name (add app.kubernetes.io/name to the allowlist)"; fi
  for m in kube_pod_container_resource_requests kube_pod_status_phase kube_pod_info; do
    if has "${m}"; then ok "${m} present"; else bad "${m} missing (kube-state-metrics)"; fi
  done
  if has 'container_cpu_usage_seconds_total{container!=""}'; then ok "cAdvisor container metrics present"; else bad "no cAdvisor container metrics"; fi

  # Which label holds the node name on cAdvisor root-cgroup series?
  nodes="$(k get nodes -o jsonpath='{.items[*].metadata.name}')"
  label=""
  for l in node instance kubernetes_io_hostname; do
    match="$(query "${PROM_URL}" /api/v1/query "count by (${l}) (container_cpu_usage_seconds_total{id=\"/\"})" \
      | python3 -c 'import json,sys
l, nodes = sys.argv[1], set(sys.argv[2].split())
try: r=json.load(sys.stdin)["data"]["result"]
except Exception: r=[]
vals={x["metric"].get(l,"") for x in r}
print(l if nodes and nodes <= vals else "")' "${l}" "${nodes}" || true)"
    if [[ -n "${match}" ]]; then label="${match}"; break; fi
  done
  if [[ -n "${label}" ]]; then ok "cAdvisor node label: ${label}  ->  config.cadvisorNodeLabel: ${label}"; else bad "no cAdvisor label matches the node names (root cgroup id=\"/\" series missing?)"; fi
else
  bad "cannot reach Prometheus through ${PROMETHEUS}"
fi

printf '\n'
[[ "${fail}" -eq 0 ]] && ok "prerequisites met" || printf '\033[31mSome prerequisites are missing; see docs/installation.md.\033[0m\n'
exit "${fail}"
