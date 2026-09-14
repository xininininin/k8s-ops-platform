#!/usr/bin/env bash
set -euo pipefail

cluster_name="${1:-k8s-ops}"
image_prefix="${IMAGE_PREFIX:-k8s-ops-platform}"
tag="${TAG:-dev}"
namespace="${NAMESPACE:-k8s-ops-platform}"

make docker-build IMAGE_PREFIX="$image_prefix" TAG="$tag"
kind load docker-image --name "$cluster_name" "${image_prefix}-agent:${tag}" "${image_prefix}-controller:${tag}"
make deploy IMAGE_PREFIX="$image_prefix" TAG="$tag" NAMESPACE="$namespace"
kubectl -n "$namespace" rollout restart deployment/diagnosis-agent deployment/action-controller
kubectl -n "$namespace" rollout status deployment/diagnosis-agent --timeout=180s
kubectl -n "$namespace" rollout status deployment/action-controller --timeout=180s
