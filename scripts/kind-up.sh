#!/usr/bin/env bash
set -euo pipefail

cluster_name="${1:-k8s-ops}"
if kind get clusters | grep -Fxq "$cluster_name"; then
  echo "Kind cluster already exists: $cluster_name"
  exit 0
fi
kind create cluster --name "$cluster_name" --config deploy/kind.yaml --wait 120s
