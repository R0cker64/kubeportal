#!/usr/bin/env bash
# Port-forwards the portal (localhost:8080) and Keycloak (localhost:8180) of
# the kind cluster until Ctrl+C. Both are needed to log in from the browser:
# the portal redirects to Keycloak's public hostname, http://localhost:8180.
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

require kubectl

PIDS=()
cleanup() { for pid in "${PIDS[@]}"; do kill "${pid}" 2>/dev/null || true; done; }
trap cleanup EXIT INT TERM

k_background -n kubeportal port-forward svc/kubeportal 8080:8080
k_background -n keycloak port-forward "svc/${KEYCLOAK_SERVICE:-keycloak}" 8180:8080

log "portal: http://localhost:8080   keycloak: http://localhost:8180   (Ctrl+C to stop)"
# Stop everything as soon as either forward exits (e.g. port already in use).
wait -n || true
die "a port-forward stopped; check that ports 8080 and 8180 are free"
