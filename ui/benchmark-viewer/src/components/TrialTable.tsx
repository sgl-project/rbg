import type { TrialResult } from '@/types'
import { formatNumber, formatDate, isSlaPass } from '@/lib/utils'
import { Card, CardHeader, CardTitle, CardContent } from '@/components/ui/card'
import { useState } from 'react'
import { X, ZoomIn } from 'lucide-react'
import { Button } from '@/components/ui/button'

interface TrialTableProps {
  trials: TrialResult[]
  optimize: string
  showTemplateColumn?: boolean
}

function StatusBadge({ pass }: { pass: boolean }) {
  return (
    <span className={`inline-flex items-center px-2 py-0.5 rounded-full text-xs font-medium ${
      pass ? 'bg-success/10 text-success border border-success/20' : 'bg-destructive/10 text-destructive border border-destructive/20'
    }`}>
      {pass ? 'PASS' : 'FAIL'}
    </span>
  )
}

function MetricCell({ value, unit }: { value: number; unit?: string }) {
  return <span className="font-mono text-xs">{formatNumber(value)}{unit}</span>
}

function TrialDetailDialog({ trial, optimize, onClose }: { trial: TrialResult; optimize: string; onClose: () => void }) {
  const m = trial.metrics
  const params = trial.params.default || {}

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center p-4 animate-fade-in">
      <div className="absolute inset-0 bg-background/80 backdrop-blur-sm" onClick={onClose} />
      <div className="relative w-full max-w-lg glass-card p-6 animate-slide-up max-h-[85vh] overflow-y-auto">
        <div className="flex items-center justify-between mb-4">
          <h3 className="text-lg font-semibold">Trial #{trial.trialIndex} Details</h3>
          <Button variant="ghost" size="icon" onClick={onClose}><X className="h-4 w-4" /></Button>
        </div>

        <div className="space-y-4">
          <section>
            <h4 className="text-xs font-semibold text-muted-foreground uppercase tracking-wider mb-2">Parameters</h4>
            <div className="grid grid-cols-2 gap-2">
              {Object.entries(params).map(([k, v]) => (
                <div key={k} className="bg-secondary/50 rounded-md px-3 py-2">
                  <div className="text-xs text-muted-foreground">{k}</div>
                  <div className="font-mono text-sm">{typeof v === 'number' ? formatNumber(v) : String(v)}</div>
                </div>
              ))}
            </div>
          </section>

          <section>
            <h4 className="text-xs font-semibold text-muted-foreground uppercase tracking-wider mb-2">Status</h4>
            <div className="grid grid-cols-3 gap-2 text-sm">
              <div><span className="text-muted-foreground">SLA:</span> <StatusBadge pass={isSlaPass(trial.constraints)} /></div>
              <div><span className="text-muted-foreground">Score:</span> <span className="font-mono">{formatNumber(trial.score)}</span></div>
              <div><span className="text-muted-foreground">Duration:</span> <span className="font-mono">{trial.duration}</span></div>
              <div className="col-span-2"><span className="text-muted-foreground">Start:</span> <span className="text-xs">{formatDate(trial.startTime)}</span></div>
              <div><span className="text-muted-foreground">End:</span> <span className="text-xs">{formatDate(trial.endTime)}</span></div>
            </div>
          </section>

          {m && (
            <section>
              <h4 className="text-xs font-semibold text-muted-foreground uppercase tracking-wider mb-2">Latency (ms)</h4>
              <div className="grid grid-cols-2 gap-2">
                <div className="bg-secondary/50 rounded-md px-3 py-2"><div className="text-xs text-muted-foreground">TTFT P50</div><div className="font-mono text-sm">{formatNumber(m.ttftP50)}</div></div>
                <div className="bg-secondary/50 rounded-md px-3 py-2"><div className="text-xs text-muted-foreground">TTFT P99</div><div className="font-mono text-sm">{formatNumber(m.ttftP99)}</div></div>
                <div className="bg-secondary/50 rounded-md px-3 py-2"><div className="text-xs text-muted-foreground">TPOT P50</div><div className="font-mono text-sm">{formatNumber(m.tpotP50)}</div></div>
                <div className="bg-secondary/50 rounded-md px-3 py-2"><div className="text-xs text-muted-foreground">TPOT P99</div><div className="font-mono text-sm">{formatNumber(m.tpotP99)}</div></div>
              </div>
            </section>
          )}

          {m && (
            <section>
              <h4 className="text-xs font-semibold text-muted-foreground uppercase tracking-wider mb-2">Throughput</h4>
              <div className="grid grid-cols-2 gap-2">
                <div className="bg-secondary/50 rounded-md px-3 py-2"><div className="text-xs text-muted-foreground">Output (tok/s)</div><div className="font-mono text-sm">{formatNumber(m.outputThroughput)}</div></div>
                <div className="bg-secondary/50 rounded-md px-3 py-2"><div className="text-xs text-muted-foreground">Input (tok/s)</div><div className="font-mono text-sm">{formatNumber(m.inputThroughput)}</div></div>
                <div className="bg-secondary/50 rounded-md px-3 py-2"><div className="text-xs text-muted-foreground">Total (tok/s)</div><div className="font-mono text-sm">{formatNumber(m.totalThroughput)}</div></div>
                <div className="bg-secondary/50 rounded-md px-3 py-2"><div className="text-xs text-muted-foreground">Req/s</div><div className="font-mono text-sm">{formatNumber(m.requestsPerSecond)}</div></div>
              </div>
            </section>
          )}

          {m && (
            <section>
              <h4 className="text-xs font-semibold text-muted-foreground uppercase tracking-wider mb-2">Requests</h4>
              <div className="grid grid-cols-3 gap-2">
                <div className="bg-secondary/50 rounded-md px-3 py-2"><div className="text-xs text-muted-foreground">Completed</div><div className="font-mono text-sm">{m.numCompletedRequests}</div></div>
                <div className="bg-secondary/50 rounded-md px-3 py-2"><div className="text-xs text-muted-foreground">Errors</div><div className="font-mono text-sm">{m.numErrorRequests}</div></div>
                <div className="bg-secondary/50 rounded-md px-3 py-2"><div className="text-xs text-muted-foreground">Total</div><div className="font-mono text-sm">{m.numRequests}</div></div>
              </div>
            </section>
          )}

          {trial.error && (
            <section>
              <h4 className="text-xs font-semibold text-destructive uppercase tracking-wider mb-1">Error</h4>
              <p className="bg-destructive/10 text-destructive rounded-md px-3 py-2 text-sm font-mono">{trial.error}</p>
            </section>
          )}
        </div>
      </div>
    </div>
  )
}

export function TrialTable({ trials, optimize, showTemplateColumn }: TrialTableProps) {
  const [selectedTrial, setSelectedTrial] = useState<TrialResult | null>(null)
  const [sortKey, setSortKey] = useState<'trialIndex' | 'score' | 'duration'>('trialIndex')
  const [sortDir, setSortDir] = useState<'asc' | 'desc'>('asc')

  const sorted = [...trials].sort((a, b) => {
    let va: number, vb: number
    if (sortKey === 'duration') {
      va = parseInt(a.duration) || 0
      vb = parseInt(b.duration) || 0
    } else {
      va = a[sortKey] as number
      vb = b[sortKey] as number
    }
    return sortDir === 'asc' ? va - vb : vb - va
  })

  const bestTrial = trials.reduce((a, b) => a.score > b.score ? a : b)

  const handleSort = (key: typeof sortKey) => {
    if (key === sortKey) setSortDir(d => d === 'asc' ? 'desc' : 'asc')
    else { setSortKey(key); setSortDir('asc') }
  }

  function SortArrow({ col }: { col: typeof sortKey }) {
    if (sortKey !== col) return null
    return <span className="ml-1 text-primary">{sortDir === 'asc' ? '↑' : '↓'}</span>
  }

  return (
    <>
      <Card>
        <CardHeader>
          <CardTitle>Trial Results ({trials.length})</CardTitle>
        </CardHeader>
        <CardContent>
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead>
                <tr className="border-b border-border text-muted-foreground text-left">
                  <th className="py-2 px-3 cursor-pointer hover:text-foreground" onClick={() => handleSort('trialIndex')}>
                    #<SortArrow col="trialIndex" />
                  </th>
                  {showTemplateColumn && <th className="py-2 px-3">Template</th>}
                  <th className="py-2 px-3">Params</th>
                  <th className="py-2 px-3 cursor-pointer hover:text-foreground" onClick={() => handleSort('score')}>
                    Score<SortArrow col="score" />
                  </th>
                  <th className="py-2 px-3">SLA</th>
                  <th className="py-2 px-3">TTFT P99</th>
                  <th className="py-2 px-3">Output Tok/s</th>
                  <th className="py-2 px-3">Error Rate</th>
                  <th className="py-2 px-3 cursor-pointer hover:text-foreground" onClick={() => handleSort('duration')}>
                    Duration<SortArrow col="duration" />
                  </th>
                  <th className="py-2 px-3"></th>
                </tr>
              </thead>
              <tbody>
                {sorted.map(t => {
                  const params = t.params.default || {}
                  const isBest = t === bestTrial
                  return (
                    <tr
                      key={t.trialIndex}
                      className={`border-b border-border/50 hover:bg-accent/50 transition-colors ${isBest ? 'bg-success/5' : ''}`}
                    >
                      <td className="py-2.5 px-3 font-mono">
                        {t.trialIndex}
                        {isBest && <span className="ml-1 text-xs text-success">★</span>}
                      </td>
                      {showTemplateColumn && <td className="py-2.5 px-3 text-xs font-medium">{t.templateName}</td>}
                      <td className="py-2.5 px-3 text-xs">
                        <div className="flex flex-wrap gap-1">
                          {Object.entries(params).map(([k, v]) => (
                            <span key={k} className="bg-secondary/60 rounded px-1.5 py-0.5">
                              {k}: {typeof v === 'number' ? formatNumber(v) : String(v)}
                            </span>
                          ))}
                        </div>
                      </td>
                      <td className="py-2.5 px-3 font-mono">{formatNumber(t.score)}</td>
                      <td className="py-2.5 px-3"><StatusBadge pass={isSlaPass(t.constraints)} /></td>
                      <td className="py-2.5 px-3 font-mono text-xs">
                        {t.metrics ? <MetricCell value={t.metrics.ttftP99} unit="ms" /> : '-'}
                      </td>
                      <td className="py-2.5 px-3 font-mono text-xs">
                        {t.metrics ? <MetricCell value={t.metrics.outputThroughput} /> : '-'}
                      </td>
                      <td className="py-2.5 px-3 font-mono text-xs">
                        {t.metrics ? <MetricCell value={t.metrics.errorRate * 100} unit="%" /> : '-'}
                      </td>
                      <td className="py-2.5 px-3 font-mono text-xs">{t.duration}</td>
                      <td className="py-2.5 px-3">
                        <Button variant="ghost" size="sm" onClick={() => setSelectedTrial(t)}>
                          <ZoomIn className="h-3.5 w-3.5" />
                        </Button>
                      </td>
                    </tr>
                  )
                })}
              </tbody>
            </table>
          </div>
        </CardContent>
      </Card>

      {selectedTrial && (
        <TrialDetailDialog trial={selectedTrial} optimize={optimize} onClose={() => setSelectedTrial(null)} />
      )}
    </>
  )
}
