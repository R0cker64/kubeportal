# Shared settings for the kind scripts. Every kubectl/helm call goes through
# the wrappers below, which use a dedicated kubeconfig and an explicit kind
# context, so the user's current context (possibly a real cluster) is never
# read or modified.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
KIND_DIR="${ROOT_DIR}/deploy/kind"
STATE_DIR="${ROOT_DIR}/.kind"
CLUSTER_NAME="kubeportal"
KCONTEXT="kind-${CLUSTER_NAME}"
export KUBECONFIG="${STATE_DIR}/kubeconfig"

# Pinned chart versions (bump deliberately, one at a time).
LOKI_CHART_VERSION="7.3.0"
ALLOY_CHART_VERSION="1.13.1"
PROMETHEUS_CHART_VERSION="29.36.1"

log() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
die() { printf '\033[1;31merror:\033[0m %s\n' "$*" >&2; exit 1; }

require() {
  for bin in "$@"; do
    command -v "${bin}" >/dev/null 2>&1 || die "missing required tool: ${bin}"
  done
}

guard_kind_context() {
  case "${KCONTEXT}" in
    kind-*) ;;
    *) die "refusing to run against non-kind context ${KCONTEXT}" ;;
  esac
  [[ "${KUBECONFIG}" == "${STATE_DIR}/kubeconfig" ]] || die "unexpected KUBECONFIG ${KUBECONFIG}"
  [[ -f "${KUBECONFIG}" ]] || die "missing ${KUBECONFIG}; run make dev-up"
  # Second line of defence: the API server must be the local kind container.
  local server
  server="$(kubectl --kubeconfig "${KUBECONFIG}" config view --raw \
    -o jsonpath="{.clusters[?(@.name==\"${KCONTEXT}\")].cluster.server}")"
  [[ "${server}" =~ ^https://(127\.0\.0\.1|localhost):[0-9]+$ ]] \
    || die "refusing to run: API server '${server}' is not a local kind cluster"
}

k() {
  guard_kind_context
  kubectl --kubeconfig "${KUBECONFIG}" --context "${KCONTEXT}" "$@"
}

# k_background starts kubectl in the background and appends its PID to the
# caller's PIDS array. Backgrounding the k function instead would record the
# PID of a subshell, and killing it would leave kubectl (e.g. a port-forward)
# running.
k_background() {
  guard_kind_context
  kubectl --kubeconfig "${KUBECONFIG}" --context "${KCONTEXT}" "$@" &
  PIDS+=("$!")
}

h() {
  guard_kind_context
  helm --kubeconfig "${KUBECONFIG}" --kube-context "${KCONTEXT}" "$@"
}

cluster_exists() {
  kind get clusters 2>/dev/null | grep -qx "${CLUSTER_NAME}"
}
