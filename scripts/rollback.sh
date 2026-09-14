#!/usr/bin/env bash
set -euo pipefail

release="${1:-k8s-ops-platform}"
namespace="${2:-k8s-ops-platform}"
previous_revision="${3:-}"
timeout="${ROLLBACK_TIMEOUT:-180s}"

command -v helm >/dev/null || { echo "ROLLBACK FAILED: missing command: helm" >&2; exit 2; }
command -v kubectl >/dev/null || { echo "ROLLBACK FAILED: missing command: kubectl" >&2; exit 2; }

if [[ -z "$previous_revision" ]]; then
  current_revision=$(helm history "$release" -n "$namespace" 2>/dev/null | awk 'NR > 1 && $1 ~ /^[0-9]+$/ {revision=$1} END {print revision}')
  [[ -n "$current_revision" ]] || { echo "ROLLBACK FAILED: release has no history" >&2; exit 1; }
  previous_revision=$((current_revision - 1))
fi

[[ "$previous_revision" =~ ^[0-9]+$ ]] || { echo "ROLLBACK FAILED: revision must be numeric" >&2; exit 2; }

if (( previous_revision < 1 )); then
  echo "ROLLBACK: no prior revision; uninstalling failed first release"
  helm uninstall "$release" -n "$namespace"
  echo "ROLLBACK SUCCEEDED: failed first release removed"
  exit 0
fi

echo "ROLLBACK: restoring ${release} revision ${previous_revision}"
helm rollback "$release" "$previous_revision" -n "$namespace" --wait --timeout "$timeout"
kubectl -n "$namespace" rollout status deployment/diagnosis-agent --timeout="$timeout"
kubectl -n "$namespace" rollout status deployment/action-controller --timeout="$timeout"
echo "ROLLBACK SUCCEEDED: ${release} is Ready at revision ${previous_revision}"
