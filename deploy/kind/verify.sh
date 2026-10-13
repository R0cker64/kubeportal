#!/usr/bin/env bash
# Checks the Phase 0/1 prerequisites inside kind: Loki has logs labelled with
# `team` and kube-state-metrics exposes label_team. Queries go through
# temporary port-forwards to the kind cluster only.
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

require kubectl curl

PIDS=()
cleanup() { for pid in "${PIDS[@]}"; do kill "${pid}" 2>/dev/null || true; done; }
trap cleanup EXIT

forward() {
  local svc="$1" local_port="$2" remote_port="$3"
  k -n monitoring port-forward "svc/${svc}" "${local_port}:${remote_port}" >/dev/null 2>&1 &
  PIDS+=("$!")
  for _ in $(seq 1 30); do
    curl -s -o /dev/null "http://127.0.0.1:${local_port}/" && return 0
    sleep 0.5
  done
  die "port-forward to ${svc} did not become ready"
}

has_results() {
  curl -sfG --max-time 10 "$@" | grep -q '"result":\[{'
}

forward loki-gateway 13100 80
forward prometheus-server 19090 80

fail=0

log "Loki: {team=\"payments\"}"
if has_results http://127.0.0.1:13100/loki/api/v1/query_range \
  --data-urlencode 'query={team="payments"}' --data-urlencode limit=5; then
  echo "ok"
else
  echo "no results"; fail=1
fi

log "Prometheus: kube_pod_labels{label_team=\"payments\"}"
if has_results http://127.0.0.1:19090/api/v1/query \
  --data-urlencode 'query=kube_pod_labels{label_team="payments"}'; then
  echo "ok"
else
  echo "no results"; fail=1
fi

exit "${fail}"
