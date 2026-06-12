#!/bin/bash
# Collect pprof profiling data from the controller.
# Run this during or after each stress test phase.
set -euo pipefail

PPROF_ADDR="${PPROF_ADDR:-localhost:6060}"
OUTPUT_DIR="${OUTPUT_DIR:-/tmp/rbg-stress-results}"
PHASE="${PHASE:-unknown}"
CPU_DURATION="${CPU_DURATION:-30}"

mkdir -p "${OUTPUT_DIR}"

echo "=== Collecting profiling data for phase: ${PHASE} ==="

# CPU profile (this blocks for CPU_DURATION seconds)
echo "[1/5] Collecting CPU profile (${CPU_DURATION}s)..."
curl -s "http://${PPROF_ADDR}/debug/pprof/profile?seconds=${CPU_DURATION}" \
    -o "${OUTPUT_DIR}/cpu-${PHASE}.prof" || echo "  WARNING: CPU profile collection failed"

# Heap (memory in-use)
echo "[2/5] Collecting heap profile..."
curl -s "http://${PPROF_ADDR}/debug/pprof/heap" \
    -o "${OUTPUT_DIR}/heap-${PHASE}.prof" || echo "  WARNING: Heap profile collection failed"

# Allocs (cumulative allocations)
echo "[3/5] Collecting allocs profile..."
curl -s "http://${PPROF_ADDR}/debug/pprof/allocs" \
    -o "${OUTPUT_DIR}/allocs-${PHASE}.prof" || echo "  WARNING: Allocs profile collection failed"

# Goroutines
echo "[4/5] Collecting goroutine profile..."
curl -s "http://${PPROF_ADDR}/debug/pprof/goroutine" \
    -o "${OUTPUT_DIR}/goroutine-${PHASE}.prof" || echo "  WARNING: Goroutine profile collection failed"

# Generate human-readable top reports for analysis
echo "[5/5] Generating top reports..."

if [ -f "${OUTPUT_DIR}/heap-${PHASE}.prof" ] && [ -s "${OUTPUT_DIR}/heap-${PHASE}.prof" ]; then
    go tool pprof -top -nodecount=30 "${OUTPUT_DIR}/heap-${PHASE}.prof" \
        > "${OUTPUT_DIR}/heap-${PHASE}-top.txt" 2>/dev/null || true
fi

if [ -f "${OUTPUT_DIR}/cpu-${PHASE}.prof" ] && [ -s "${OUTPUT_DIR}/cpu-${PHASE}.prof" ]; then
    go tool pprof -top -nodecount=30 "${OUTPUT_DIR}/cpu-${PHASE}.prof" \
        > "${OUTPUT_DIR}/cpu-${PHASE}-top.txt" 2>/dev/null || true
fi

if [ -f "${OUTPUT_DIR}/goroutine-${PHASE}.prof" ] && [ -s "${OUTPUT_DIR}/goroutine-${PHASE}.prof" ]; then
    go tool pprof -top -nodecount=20 "${OUTPUT_DIR}/goroutine-${PHASE}.prof" \
        > "${OUTPUT_DIR}/goroutine-${PHASE}-top.txt" 2>/dev/null || true
fi

echo ""
echo "=== Profiling data collected ==="
echo "Output: ${OUTPUT_DIR}/"
ls -lh "${OUTPUT_DIR}"/*-${PHASE}* 2>/dev/null || true
