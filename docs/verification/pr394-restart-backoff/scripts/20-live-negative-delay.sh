#!/usr/bin/env bash
# Layer 3 scenario (B4): the apiserver rejects a negative baseDelaySeconds on a
# RoleBasedGroup (pattern field has minimum:0) but ACCEPTS it on a RoleInstance
# (spec field has no minimum). The accepted negative value then makes the backoff
# math negative, i.e. backoff is bypassed.
set -euo pipefail
export KUBECONFIG="${KUBECONFIG:-$HOME/.kube/rbg}"
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../.." && pwd)"
RESULTS="$REPO_ROOT/docs/verification/pr394-restart-backoff/results"
OUT="$RESULTS/live-negative-delay.log"
NS=rbg-verify
: >"$OUT"
log() { echo "[$(date +%T)] $*" | tee -a "$OUT"; }

log "=== (a) RBG with baseDelaySeconds=-30 (expect REJECT) ==="
cat <<'YAML' | kubectl apply -f - 2>&1 | tee -a "$OUT" || true
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleBasedGroup
metadata: { name: b4-rbg, namespace: rbg-verify }
spec:
  roles:
    - name: worker
      replicas: 1
      leaderWorkerPattern:
        size: 1
        restartPolicy: RecreateRoleInstanceOnPodRestart
        baseDelaySeconds: -30
        template:
          spec:
            containers:
              - name: nginx
                image: registry.cn-hangzhou.aliyuncs.com/acs-sample/nginx:latest
YAML

log ""
log "=== (b) RoleInstance with baseDelaySeconds=-30 (expect ACCEPT = validation gap) ==="
cat <<'YAML' | kubectl apply -f - 2>&1 | tee -a "$OUT" || true
apiVersion: workloads.x-k8s.io/v1alpha2
kind: RoleInstance
metadata: { name: b4-ri, namespace: rbg-verify }
spec:
  restartPolicy: RecreateRoleInstanceOnPodRestart
  baseDelaySeconds: -30
  maxDelaySeconds: 600
  components:
    - name: main
      size: 1
      template:
        spec:
          containers:
            - name: nginx
              image: registry.cn-hangzhou.aliyuncs.com/acs-sample/nginx:latest
YAML

log ""
log "=== resulting objects ==="
kubectl -n $NS get roleinstance b4-ri -o jsonpath='{.metadata.name}{"  baseDelaySeconds="}{.spec.baseDelaySeconds}{"\n"}' 2>&1 | tee -a "$OUT" || true

# cleanup these probe objects
kubectl -n $NS delete roleinstance b4-ri --ignore-not-found >/dev/null 2>&1 || true
kubectl -n $NS delete rbg b4-rbg --ignore-not-found >/dev/null 2>&1 || true
