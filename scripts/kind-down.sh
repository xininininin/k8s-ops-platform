#!/usr/bin/env bash
set -euo pipefail

cluster_name="${1:-k8s-ops}"
if kind get clusters | grep -Fxq "$cluster_name"; then
  kind delete cluster --name "$cluster_name"
else
  echo "Kind cluster does not exist: $cluster_name"
fi
