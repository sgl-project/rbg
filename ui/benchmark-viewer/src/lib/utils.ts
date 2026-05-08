import { type ClassValue, clsx } from "clsx"
import { twMerge } from "tailwind-merge"

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs))
}

export function formatDuration(ms: number): string {
  const seconds = Math.floor(ms / 1000)
  const minutes = Math.floor(seconds / 60)
  const hours = Math.floor(minutes / 60)
  if (hours > 0) return `${hours}h ${minutes % 60}m`
  if (minutes > 0) return `${minutes}m ${seconds % 60}s`
  return `${seconds}s`
}

export function formatDate(iso: string): string {
  try {
    const d = new Date(iso)
    return d.toLocaleString('zh-CN', {
      month: 'short', day: 'numeric',
      hour: '2-digit', minute: '2-digit', second: '2-digit',
    })
  } catch { return iso }
}

export function formatNumber(n: number, decimals = 2): string {
  return Number.isInteger(n) ? n.toString() : n.toFixed(decimals)
}

/**
 * Derive SLA pass status from constraints array.
 * constraints[i] > 0 means the i-th SLA constraint is violated.
 * Returns true if all constraints are <= 0 (no violations).
 * Returns false if any constraint > 0.
 * Returns false if constraints is null/undefined (trial errored out).
 */
export function isSlaPass(constraints: number[] | null | undefined): boolean {
  if (!constraints || constraints.length === 0) return false
  return constraints.every(c => c <= 0)
}

// Metrics that should be minimized (lower is better)
const MINIMIZE_METRICS = [
  'ttftP50', 'ttftP99',
  'tpotP50', 'tpotP99',
  'errorRate',
  'numErrorRequests',
]

/**
 * Determine if a metric is "lower is better".
 * Defaults to "higher is better" for throughput/score metrics.
 */
export function isMinimizeMetric(metric: string): boolean {
  return MINIMIZE_METRICS.includes(metric)
}

/**
 * Compute a 0-1 ratio for a score relative to the trial set's min/max,
 * considering optimization direction.
 * 
 * - For "higher is better" metrics: max score → ratio 1, min → ratio 0
 * - For "lower is better" metrics: min score → ratio 1, max → ratio 0
 * 
 * Returns ratio clamped to [0, 1]. If all scores are equal, returns 0.5.
 */
export function computeScoreRatio(scores: number[], score: number, optimize: string): number {
  const min = Math.min(...scores)
  const max = Math.max(...scores)
  if (min === max) return 0.5
  const ratio = isMinimizeMetric(optimize)
    ? (max - score) / (max - min)  // lower is better: invert
    : (score - min) / (max - min)  // higher is better
  return Math.max(0, Math.min(1, ratio))
}

/**
 * Map a 0-1 ratio to a color.
 * Green (ratio 0 = low/good) → Red (ratio 1 = high/bad for minimize, or high score for maximize).
 * 
 * Wait — re-reading the user request: red = high score, green = low score.
 * So for "higher is better": red = best, green = worst.
 * For "lower is better": green = best (low value), red = worst (high value).
 * 
 * Actually the user said: "红色为高分，绿色为低分" — red = high score, green = low score.
 * This is a simple mapping regardless of optimization direction.
 * The optimization direction is already handled in computeScoreRatio.
 */
export function scoreColorToHsl(ratio: number, slaPass: boolean): string {
  if (!slaPass) return 'hsl(0, 0%, 45%)' // gray for SLA fail
  // Green (low score, ratio=0) → Red (high score, ratio=1)
  const h = 120 - 120 * ratio  // 120 (green) → 0 (red)
  const s = 65
  const l = 48
  return `hsl(${h}, ${s}%, ${l}%)`
}
