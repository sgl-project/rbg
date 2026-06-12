#!/bin/bash
# Collect and classify controller logs for stress test analysis.
set -euo pipefail

NAMESPACE="${NAMESPACE:-rbgs-system}"
OUTPUT_DIR="${OUTPUT_DIR:-/tmp/rbg-stress-results}"
SINCE="${SINCE:-10m}"

mkdir -p "${OUTPUT_DIR}"

echo "=== Collecting controller logs ==="

# Fetch logs from controller pod(s)
echo "[1/3] Fetching controller logs (since ${SINCE})..."
kubectl logs -n "${NAMESPACE}" -l control-plane=rbgs-controller \
    --since="${SINCE}" --tail=-1 \
    > "${OUTPUT_DIR}/controller-full.log" 2>&1 || true

TOTAL_LINES=$(wc -l < "${OUTPUT_DIR}/controller-full.log" | tr -d ' ')
echo "  Total log lines: ${TOTAL_LINES}"

# Extract errors
echo "[2/3] Extracting and classifying errors..."
grep -i "error\|ERROR\|failed\|Failed\|panic\|PANIC" "${OUTPUT_DIR}/controller-full.log" \
    > "${OUTPUT_DIR}/errors.log" 2>/dev/null || true

ERROR_COUNT=$(wc -l < "${OUTPUT_DIR}/errors.log" | tr -d ' ')
echo "  Total error lines: ${ERROR_COUNT}"

# Classify errors
echo "[3/3] Generating error classification..."
cat > "${OUTPUT_DIR}/error-summary.txt" << 'HEADER'
=== RBG Controller Error Classification ===

HEADER

# Use a subshell without pipefail for classification (grep returns 1 on no match)
(
    set +o pipefail

    echo "--- Reconcile Errors ---"
    count=$(grep -c -iE "Reconciler error|failed to reconcile|reconcile error|Failed to create workload" "${OUTPUT_DIR}/errors.log" 2>/dev/null || echo "0")
    echo "Count: ${count}"
    grep -iE "Reconciler error|failed to reconcile|Failed to create workload" "${OUTPUT_DIR}/errors.log" 2>/dev/null | head -3
    echo ""

    echo "--- Unsupported Workload Type ---"
    count=$(grep -c -E "unsupported workload type" "${OUTPUT_DIR}/errors.log" 2>/dev/null || echo "0")
    echo "Count: ${count}"
    grep -E "unsupported workload type" "${OUTPUT_DIR}/errors.log" 2>/dev/null | head -3
    echo ""

    echo "--- Conflict Errors (optimistic locking) ---"
    count=$(grep -c -E "conflict|the object has been modified|StorageError|resource version" "${OUTPUT_DIR}/errors.log" 2>/dev/null || echo "0")
    echo "Count: ${count}"
    grep -E "conflict|the object has been modified|StorageError" "${OUTPUT_DIR}/errors.log" 2>/dev/null | head -3
    echo ""

    echo "--- API Throttling ---"
    count=$(grep -c -E "throttl|rate.*limit|too many requests|429|client-side throttling" "${OUTPUT_DIR}/errors.log" 2>/dev/null || echo "0")
    echo "Count: ${count}"
    grep -E "throttl|rate.*limit|client-side throttling" "${OUTPUT_DIR}/errors.log" 2>/dev/null | head -3
    echo ""

    # Also check full logs for throttle warnings (they may not be "error" level)
    throttle_warnings=$(grep -c -E "Waited for|throttling" "${OUTPUT_DIR}/controller-full.log" 2>/dev/null || echo "0")
    echo "Throttle warnings (all levels): ${throttle_warnings}"
    grep -E "Waited for|throttling" "${OUTPUT_DIR}/controller-full.log" 2>/dev/null | head -3
    echo ""

    echo "--- Context Deadline / Timeout ---"
    count=$(grep -c -E "context deadline|context canceled|timeout|timed out" "${OUTPUT_DIR}/errors.log" 2>/dev/null || echo "0")
    echo "Count: ${count}"
    grep -E "context deadline|context canceled|timeout" "${OUTPUT_DIR}/errors.log" 2>/dev/null | head -3
    echo ""

    echo "--- Not Found ---"
    count=$(grep -c -E "not found|NotFound" "${OUTPUT_DIR}/errors.log" 2>/dev/null || echo "0")
    echo "Count: ${count}"
    grep -E "not found|NotFound" "${OUTPUT_DIR}/errors.log" 2>/dev/null | head -3
    echo ""

    echo "--- OOM / Memory Pressure ---"
    count=$(grep -c -E "OOM|out of memory|memory pressure|killed" "${OUTPUT_DIR}/errors.log" 2>/dev/null || echo "0")
    echo "Count: ${count}"
    grep -E "OOM|out of memory|memory pressure" "${OUTPUT_DIR}/errors.log" 2>/dev/null | head -3
    echo ""

    echo "--- Panics ---"
    count=$(grep -c -E "panic|PANIC|runtime error" "${OUTPUT_DIR}/errors.log" 2>/dev/null || echo "0")
    echo "Count: ${count}"
    grep -E "panic|PANIC|runtime error" "${OUTPUT_DIR}/errors.log" 2>/dev/null | head -5
    echo ""

) >> "${OUTPUT_DIR}/error-summary.txt"

echo ""
echo "=== Log collection complete ==="
echo "Files:"
echo "  ${OUTPUT_DIR}/controller-full.log (${TOTAL_LINES} lines)"
echo "  ${OUTPUT_DIR}/errors.log (${ERROR_COUNT} lines)"
echo "  ${OUTPUT_DIR}/error-summary.txt"
