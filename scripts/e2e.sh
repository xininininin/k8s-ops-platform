#!/usr/bin/env bash
set -euo pipefail

cluster_name="${1:-k8s-ops}"
platform_namespace="${PLATFORM_NAMESPACE:-k8s-ops-platform}"
for command_name in docker kind kubectl curl; do
  command -v "$command_name" >/dev/null || { echo "missing command: $command_name" >&2; exit 2; }
done
kind get clusters | grep -Fxq "$cluster_name" || { echo "Kind cluster not found: $cluster_name" >&2; exit 2; }

if [[ "${SKIP_DEPLOY:-false}" != "true" ]]; then
  ./scripts/deploy-kind.sh "$cluster_name"
fi
kubectl delete actions --all --all-namespaces --ignore-not-found
kubectl delete deployment crashloop-example e2e-web e2e-slow --ignore-not-found
kubectl apply -f test/e2e/workloads.yaml
kubectl rollout status deployment/e2e-web --timeout=180s
kubectl rollout status deployment/e2e-slow --timeout=180s

echo "CASE 1: CrashLoop diagnosis, log and metric"
kubectl apply -f examples/faults/crashloop.yaml
deadline=$((SECONDS + 180))
until kubectl -n "$platform_namespace" logs deployment/diagnosis-agent --since=5m | grep '"reason":"CrashLoopBackOff"' >/dev/null; do
  (( SECONDS < deadline )) || { echo "CASE 1 FAILED: diagnosis log timeout" >&2; exit 1; }
  sleep 3
done
kubectl -n "$platform_namespace" port-forward service/diagnosis-agent 18080:8080 >test/e2e/port-forward.log 2>&1 &
forward_pid=$!
trap 'kill "$forward_pid" 2>/dev/null || true' EXIT
deadline=$((SECONDS + 30))
until curl -fsS localhost:18080/metrics | grep 'k8s_ops_diagnosis_total{[^}]*reason="CrashLoopBackOff"' >/dev/null; do
  (( SECONDS < deadline )) || { echo "CASE 1 FAILED: diagnosis metric timeout" >&2; exit 1; }
  sleep 1
done
kill "$forward_pid" 2>/dev/null || true
trap - EXIT
echo "CASE 1 PASSED"

echo "CASE 2: PodRestart full state machine"
old_pod=$(kubectl get pod -l app=e2e-web -o jsonpath='{.items[0].metadata.name}')
old_uid=$(kubectl get pod "$old_pod" -o jsonpath='{.metadata.uid}')
kubectl get actions -w -o custom-columns=NAME:.metadata.name,PHASE:.status.phase --no-headers >test/e2e/action-phases.log 2>&1 &
phase_watch_pid=$!
kubectl apply -f - <<EOF
apiVersion: ops.example.io/v1alpha1
kind: Action
metadata: {name: e2e-pod-restart, namespace: default}
spec:
  actionType: PodRestart
  target: {kind: Pod, namespace: default, name: ${old_pod}}
  reason: e2e
  timeoutSeconds: 180
  requestedBy: e2e
  triggerSource: E2E
EOF
kubectl wait --for=jsonpath='{.status.phase}'=Succeeded action/e2e-pod-restart --timeout=180s
sleep 1
kill "$phase_watch_pid" 2>/dev/null || true
for expected_phase in Pending Running Verifying Succeeded; do
  grep "e2e-pod-restart[[:space:]].*${expected_phase}" test/e2e/action-phases.log >/dev/null || { echo "CASE 2 FAILED: phase ${expected_phase} not observed" >&2; exit 1; }
done
new_uids=$(kubectl get pod -l app=e2e-web -o jsonpath='{range .items[*]}{.metadata.uid}{"\n"}{end}')
if ! grep -Fxv "$old_uid" <<<"$new_uids" | grep '.' >/dev/null; then
  echo "CASE 2 FAILED: no replacement Pod UID found" >&2
  exit 1
fi
echo "CASE 2 PASSED"

echo "CASE 3: NodeMaintenance cordon/drain/hook/health/uncordon"
worker=$(kubectl get nodes -l '!node-role.kubernetes.io/control-plane' -o jsonpath='{.items[0].metadata.name}')
kubectl apply -f - <<EOF
apiVersion: ops.example.io/v1alpha1
kind: Action
metadata: {name: e2e-node-maintenance, namespace: default}
spec:
  actionType: NodeMaintenance
  target: {kind: Node, name: ${worker}}
  reason: e2e
  timeoutSeconds: 300
  requestedBy: e2e
  triggerSource: E2E
EOF
kubectl wait --for=jsonpath='{.status.phase}'=Succeeded action/e2e-node-maintenance --timeout=300s
[[ "$(kubectl get action e2e-node-maintenance -o jsonpath='{.status.executionState}')" == "Uncordoned" ]] || { echo "CASE 3 FAILED: final checkpoint is not Uncordoned" >&2; exit 1; }
[[ "$(kubectl get node "$worker" -o jsonpath='{.spec.unschedulable}')" != "true" ]] || { echo "CASE 3 FAILED: node remains cordoned" >&2; exit 1; }
echo "CASE 3 PASSED"

echo "CASE 4: Controller restart recovery from Verifying"
slow_pod=$(kubectl get pod -l app=e2e-slow -o jsonpath='{.items[0].metadata.name}')
kubectl apply -f - <<EOF
apiVersion: ops.example.io/v1alpha1
kind: Action
metadata: {name: e2e-restart-recovery, namespace: default}
spec:
  actionType: PodRestart
  target: {kind: Pod, namespace: default, name: ${slow_pod}}
  reason: restart-recovery-e2e
  timeoutSeconds: 240
  requestedBy: e2e
  triggerSource: E2E
EOF
kubectl wait --for=jsonpath='{.status.phase}'=Verifying action/e2e-restart-recovery --timeout=120s
kubectl -n "$platform_namespace" scale deployment/action-controller --replicas=0
kubectl -n "$platform_namespace" wait --for=delete pod -l app.kubernetes.io/name=action-controller --timeout=120s
kubectl -n "$platform_namespace" scale deployment/action-controller --replicas=1
kubectl -n "$platform_namespace" rollout status deployment/action-controller --timeout=180s
kubectl wait --for=jsonpath='{.status.phase}'=Succeeded action/e2e-restart-recovery --timeout=240s
echo "CASE 4 PASSED"

echo "ALL 4 E2E CASES PASSED"
