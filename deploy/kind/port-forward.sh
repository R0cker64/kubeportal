#!/usr/bin/env bash
# Port-forwards a kind service: port-forward.sh <namespace> <service> <local:remote>
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

require kubectl
[[ $# -eq 3 ]] || die "usage: $0 <namespace> <service> <local:remote>"
k -n "$1" port-forward "svc/$2" "$3"
