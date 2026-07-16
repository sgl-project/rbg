#!/usr/bin/env bash
# Layer 3 scenario: demonstrate restart backoff end-to-end on the live cluster,
# and surface the off-by-one (B5): the first realized backoff delay is 2*base.
#
# Sequence:
#   crash #1  -> first crash, no backoff (LastRestartTime was nil) => ~immediate recreation
#   crash #2  -> within the backoff window => recreation is delayed; the crashed
#                pod is preserved during the window; controller logs delay=2*base.
#
# Crash mechanism: `kubectl exec ... -- sh -c 'kill 1'` kills the container's
# PID 1 (nginx). With restartPolicy=Always the container restarts, so its
# RestartCount becomes >0, which is what RecreateRoleInstanceOnPodRestart detects.
set -euo pipefail
export KUBECONFIG="${KUBECONFIG:-$HOME/.kube/rbg}"
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../.." && pwd)"
RESULTS="$REPO_ROOT/docs/verification/pr394-restart-backoff/results"
LOG="$RESULTS/controller.log"
NS=rbg-verify
RI=backoff-demo-worker-0
SEL="rbg.workloads.x-k8s.io/role-instance-name=$RI"
OUT="$RESULTS/live-backoff.log"
: >"$OUT"

log() { echo "[$(date +%T)] $*" | tee -a "$OUT"; }

pod_name() { kubectl -n $NS get pods -l "$SEL" -o jsonpath='{.items[0].metadata.name}' 2>/dev/null; }
pod_uid()  { kubectl -n $NS get pods -l "$SEL" -o jsonpath='{.items[0].metadata.uid}'  2>/dev/null; }
ri_rc()    { kubectl -n $NS get roleinstance $RI -o jsonpath='{.status.restartCount}'   2>/dev/null; }
ri_lrt()   { kubectl -n $NS get roleinstance $RI -o jsonpath='{.status.lastRestartTime}' 2>/dev/null; }

wait_ready() { # RI condition type is RoleInstanceReady (not "Ready")
  for _ in $(seq 1 60); do
    [ "$(kubectl -n $NS get roleinstance $RI -o jsonpath='{.status.conditions[?(@.type=="RoleInstanceReady")].status}' 2>/dev/null)" = "True" ] && return 0
    sleep 2
  done
  return 1
}
make_running() { # kubelet drives readiness on a real cluster; just wait for Running
  for _ in $(seq 1 60); do
    [ "$(kubectl -n $NS get pod "$(pod_name)" -o jsonpath='{.status.phase}' 2>/dev/null)" = "Running" ] && return 0
    sleep 2
  done
  return 1
}

crash() { # $1 = label
  local old_uid="$1"
  local p; p="$(pod_name)"
  log "CRASH on pod=$p (uid=$old_uid): kill 1"
  kubectl -n $NS exec "$p" -c nginx -- sh -c 'kill 1' >/dev/null 2>&1 || log "  (exec returned non-zero, container likely already exiting)"
}

measure_recreation() { # $1 = old uid, $2 = start epoch
  local old_uid="$1" t0="$2"
  for _ in $(seq 1 180); do
    local cur; cur="$(pod_uid)"
    if [ -n "$cur" ] && [ "$cur" != "$old_uid" ]; then
      echo $(( $(date +%s) - t0 ))
      return 0
    fi
    sleep 1
  done
  echo "-1"
}

log "=== initial ==="; log "restartCount=$(ri_rc) lastRestartTime=$(ri_lrt) pod=$(pod_name)"

# ---- crash #1 : expect immediate recreation, no backoff ----
U1="$(pod_uid)"; T1=$(date +%s)
crash "$U1"
D1="$(measure_recreation "$U1" "$T1")"
log "crash#1 -> recreation took ${D1}s ; restartCount now=$(ri_rc) lastRestartTime=$(ri_lrt)"
wait_ready && make_running || log "warn: not ready after crash#1"

# ---- crash #2 : do it immediately to stay inside the (2*base) window ----
U2="$(pod_uid)"; T2=$(date +%s)
crash "$U2"
# Snapshot: is the crashed/old pod preserved while backoff is pending?
sleep 5
log "5s after crash#2: pods present ="
kubectl -n $NS get pods -l "$SEL" -o wide 2>&1 | tee -a "$OUT"
D2="$(measure_recreation "$U2" "$T2")"
log "crash#2 -> recreation took ${D2}s ; restartCount now=$(ri_rc)"

log "=== controller backoff log lines ==="
grep -iE "Restart backoff|ReCreateInstance|restartCount=" "$LOG" | tail -15 | tee -a "$OUT"

log "=== roleinstance status ==="
kubectl -n $NS get roleinstance $RI -o jsonpath='{.status.restartCount}{"\t"}{.status.lastRestartTime}{"\n"}' | tee -a "$OUT"
