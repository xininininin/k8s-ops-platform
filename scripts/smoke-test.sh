#!/usr/bin/env bash
set -euo pipefail

namespace="${1:-k8s-ops-platform}"
timeout="${SMOKE_TIMEOUT:-180s}"
smoke_workload_image="${SMOKE_WORKLOAD_IMAGE:-registry.k8s.io/pause:3.10.1}"
smoke_name="cicd-smoke"
port_forward_pids=()

fail() { echo "SMOKE FAILED: $*" >&2; exit 1; }
pass() { echo "SMOKE PASSED: $*"; }
cleanup() {
  for pid in "${port_forward_pids[@]:-}"; do kill "$pid" 2>/dev/null || true; done
  kubectl -n default delete action "$smoke_name" deployment "$smoke_name" --ignore-not-found --wait=false >/dev/null 2>&1 || true
}
trap cleanup EXIT

for command_name in kubectl curl; do
  command -v "$command_name" >/dev/null || fail "missing command: $command_name"
done

kubectl -n "$namespace" rollout status deployment/diagnosis-agent --timeout="$timeout" || fail "Agent Deployment is not Ready"
pass "Agent Pod Ready"
kubectl -n "$namespace" rollout status deployment/action-controller --timeout="$timeout" || fail "Controller Deployment is not Ready"
pass "Controller Pod Ready"

kubectl get crd actions.ops.example.io >/dev/null || fail "Action CRD is not registered"
api_resources="$(kubectl api-resources --api-group=ops.example.io -o name)"
grep -Fxq actions.ops.example.io <<<"$api_resources" || fail "Action API is not discoverable"
pass "Action CRD registered"

kubectl -n "$namespace" get serviceaccount diagnosis-agent action-controller >/dev/null || fail "ServiceAccounts are missing"
kubectl auth can-i get pods --as="system:serviceaccount:${namespace}:diagnosis-agent" --all-namespaces | grep -Fxq yes || fail "Agent cannot read Pods"
kubectl auth can-i get nodes --as="system:serviceaccount:${namespace}:diagnosis-agent" | grep -Fxq yes || fail "Agent cannot read Nodes"
kubectl auth can-i patch deployments.apps --as="system:serviceaccount:${namespace}:action-controller" --all-namespaces | grep -Fxq yes || fail "Controller cannot patch Deployments"
pass "ServiceAccount and RBAC checks"

deadline=$((SECONDS + 60))
until agent_logs="$(kubectl -n "$namespace" logs deployment/diagnosis-agent 2>/dev/null)" && grep 'diagnosis agent started' <<<"$agent_logs" >/dev/null; do
  (( SECONDS < deadline )) || fail "Agent did not confirm informer cache startup"
  sleep 2
done
pass "Agent started Kubernetes informer reads"

kubectl -n default apply -f - <<EOF
apiVersion: apps/v1
kind: Deployment
metadata:
  name: ${smoke_name}
spec:
  replicas: 1
  selector:
    matchLabels: {app: ${smoke_name}}
  template:
    metadata:
      labels: {app: ${smoke_name}}
    spec:
      containers:
        - name: web
          image: ${smoke_workload_image}
EOF
kubectl -n default rollout status deployment/"$smoke_name" --timeout="$timeout" || fail "smoke workload is not Ready"
kubectl -n default apply -f - <<EOF
apiVersion: ops.example.io/v1alpha1
kind: Action
metadata:
  name: ${smoke_name}
spec:
  actionType: DeploymentRestart
  target: {kind: Deployment, namespace: default, name: ${smoke_name}}
  reason: release-smoke-test
  timeoutSeconds: 120
  requestedBy: ci
  triggerSource: SmokeTest
EOF
kubectl -n default wait --for=jsonpath='{.status.phase}'=Succeeded action/"$smoke_name" --timeout="$timeout" || {
  kubectl -n default get action "$smoke_name" -o yaml >&2 || true
  fail "Controller did not process the minimal Action"
}
pass "Controller processed a minimal Action CR"

check_endpoint() {
  local service="$1" local_port="$2" path="$3"
  kubectl -n "$namespace" port-forward "service/${service}" "${local_port}:8080" >"/tmp/${service}-smoke-port-forward.log" 2>&1 &
  local pid=$!
  port_forward_pids+=("$pid")
  local deadline=$((SECONDS + 30))
  until curl -fsS "http://127.0.0.1:${local_port}${path}" >/dev/null 2>&1; do
    (( SECONDS < deadline )) || fail "${service}${path} is unreachable"
    sleep 1
  done
  kill "$pid" 2>/dev/null || true
  wait "$pid" 2>/dev/null || true
  pass "${service}${path} reachable"
}

check_endpoint diagnosis-agent 18080 /healthz
check_endpoint diagnosis-agent 18080 /metrics
check_endpoint diagnosis-agent 18080 /state
check_endpoint action-controller 18081 /metrics

kubectl -n "$namespace" port-forward deployment/action-controller 18082:8081 >"/tmp/action-controller-health-smoke-port-forward.log" 2>&1 &
health_pid=$!
port_forward_pids+=("$health_pid")
health_deadline=$((SECONDS + 30))
until curl -fsS "http://127.0.0.1:18082/healthz" >/dev/null 2>&1 && curl -fsS "http://127.0.0.1:18082/readyz" >/dev/null 2>&1; do
  (( SECONDS < health_deadline )) || fail "action-controller healthz/readyz is unreachable"
  sleep 1
done
kill "$health_pid" 2>/dev/null || true
wait "$health_pid" 2>/dev/null || true
pass "action-controller/healthz and /readyz reachable"
echo "SMOKE TEST PASSED"
