import { useMemo, useState } from 'react'
import { Bar, Scatter } from 'react-chartjs-2'
import {
  Chart as ChartJS,
  CategoryScale,
  LogarithmicScale,
  LinearScale,
  BarElement,
  PointElement,
  Title,
  Tooltip,
  Legend,
} from 'chart.js'
import type { TrialResult } from '@/types'
import { Card, CardHeader, CardTitle, CardContent } from '@/components/ui/card'
import { formatNumber, computeScoreRatio, scoreColorToHsl, isSlaPass } from '@/lib/utils'

ChartJS.register(CategoryScale, LogarithmicScale, LinearScale, BarElement, PointElement, Title, Tooltip, Legend)

interface ParamComparisonProps {
  trials: TrialResult[]
  optimize: string
  searchSpace?: Record<string, { type: string; values?: (number | string)[]; min?: number; max?: number }>
}

function paramNameLabel(name: string): string {
  return name.replace(/([A-Z])/g, ' $1').replace(/^./, s => s.toUpperCase()).trim()
}

export function ParamComparison({ trials, optimize, searchSpace }: ParamComparisonProps) {
  const [sortBy, setSortBy] = useState<'id' | 'score'>('id')

  const sortedTrials = useMemo(() => {
    const sorted = [...trials]
    if (sortBy === 'score') {
      sorted.sort((a, b) => b.score - a.score)
    } else {
      sorted.sort((a, b) => a.trialIndex - b.trialIndex)
    }
    return sorted
  }, [trials, sortBy])

  const allParams = useMemo(() => {
    const names = new Set<string>()
    sortedTrials.forEach(t => {
      const p = t.params.default || {}
      Object.keys(p).forEach(k => names.add(k))
    })
    return Array.from(names)
  }, [sortedTrials])

  // Detect parameter types from searchSpace or by scanning values
  const paramTypes = useMemo(() => {
    const types: Record<string, 'numeric' | 'categorical' | 'pow2'> = {}
    allParams.forEach(name => {
      const ss = searchSpace?.[name]
      if (ss) {
        if (ss.type === 'categorical' || ss.type === 'pow2') {
          types[name] = ss.type as 'categorical' | 'pow2'
          return
        }
      }
      let hasString = false
      sortedTrials.forEach(t => {
        const v = (t.params.default || {})[name]
        if (typeof v === 'string') hasString = true
      })
      types[name] = hasString ? 'categorical' : 'numeric'
    })
    return types
  }, [allParams, sortedTrials, searchSpace])

  const scores = useMemo(() => sortedTrials.map(t => t.score), [sortedTrials])

  const colorMap = useMemo(() => {
    return sortedTrials.map(t => {
      const ratio = computeScoreRatio(scores, t.score, optimize)
      return scoreColorToHsl(ratio, isSlaPass(t.constraints))
    })
  }, [sortedTrials, scores, optimize])

  // Separate numeric and categorical params
  const numericParams = allParams.filter(n => paramTypes[n] !== 'categorical')
  const categoricalParams = allParams.filter(n => paramTypes[n] === 'categorical')

  if (allParams.length === 0) {
    return (
      <Card>
        <CardHeader><CardTitle>Parameter Impact</CardTitle></CardHeader>
        <CardContent><p className="text-muted-foreground text-sm">No parameters to compare.</p></CardContent>
      </Card>
    )
  }

  return (
    <Card>
      <CardHeader>
        <div className="flex items-center justify-between">
          <CardTitle>Parameter Impact</CardTitle>
          <div className="flex items-center gap-1 bg-secondary/50 rounded-md p-0.5">
            <button
              onClick={() => setSortBy('id')}
              className={`px-2.5 py-1 text-xs rounded-md transition-colors ${
                sortBy === 'id'
                  ? 'bg-primary/10 text-primary font-medium'
                  : 'hover:bg-accent text-muted-foreground'
              }`}
            >
              By Trial ID
            </button>
            <button
              onClick={() => setSortBy('score')}
              className={`px-2.5 py-1 text-xs rounded-md transition-colors ${
                sortBy === 'score'
                  ? 'bg-primary/10 text-primary font-medium'
                  : 'hover:bg-accent text-muted-foreground'
              }`}
            >
              By Score
            </button>
          </div>
        </div>
      </CardHeader>
      <CardContent>
        <div className="grid gap-4" style={{ gridTemplateColumns: `repeat(${Math.min(allParams.length, 2)}, 1fr)` }}>
          {allParams.map(name => {
            const pType = paramTypes[name]
            const isCategorical = pType === 'categorical'
            const isPow2 = pType === 'pow2'

            if (isCategorical) {
              // Categorical: scatter chart with category scales on both axes
              const categories = Array.from(new Set(
                sortedTrials.map(t => String((t.params.default || {})[name] ?? ''))
              )).filter(Boolean)

              const trialLabels = sortedTrials.map(t => `T${t.trialIndex}`)

              const points = sortedTrials.map((t, idx) => {
                const p = t.params.default || {}
                const cat = String(p[name] ?? '')
                const ratio = computeScoreRatio(scores, t.score, optimize)
                const color = scoreColorToHsl(ratio, isSlaPass(t.constraints))
                return { x: trialLabels[idx], y: cat, score: t.score, category: cat, trialIndex: t.trialIndex, color }
              })

              const chartData = {
                datasets: [{
                  data: points.map(p => ({ x: p.x, y: p.y })),
                  backgroundColor: points.map(p => p.color),
                  pointRadius: 6,
                  pointHoverRadius: 8,
                }],
              }

              return (
                <div key={`${name}-${sortBy}`}>
                  <div className="text-xs font-medium text-muted-foreground mb-1">{paramNameLabel(name)}</div>
                  <div className="h-48">
                    <Scatter
                      key={`${name}-${sortBy}`}
                      data={chartData}
                      options={{
                        responsive: true,
                        maintainAspectRatio: false,
                        animation: false,
                        scales: {
                          x: {
                            type: 'category' as const,
                            labels: trialLabels,
                            offset: true,
                            grid: { display: false },
                            ticks: {
                              color: 'hsl(215, 20%, 60%)',
                              font: { size: 10 },
                              maxRotation: 45,
                              minRotation: 0,
                            },
                          },
                          y: {
                            type: 'category' as const,
                            labels: categories,
                            offset: true,
                            grid: { color: 'hsl(217, 33%, 18%)' },
                            ticks: {
                              color: 'hsl(215, 20%, 60%)',
                              font: { size: 10 },
                              align: 'center' as const,
                            },
                          },
                        },
                        plugins: {
                          legend: { display: false },
                          tooltip: {
                            callbacks: {
                              title: (items) => {
                                const idx = items[0]?.dataIndex
                                const p = points[idx]
                                return p ? `Trial #${p.trialIndex}` : ''
                              },
                              label: (ctx) => {
                                const p = points[ctx.dataIndex]
                                if (!p) return ''
                                return [
                                  `${paramNameLabel(name)}: ${p.category}`,
                                  `Score: ${formatNumber(p.score)}`,
                                ]
                              },
                            },
                          },
                        },
                      }}
                    />
                  </div>
                </div>
              )
            }

            // Numeric/Pow2: bar chart
            const labels = sortedTrials.map(t => `T${t.trialIndex}`)
            const dataValues = sortedTrials.map(t => {
              const p = t.params.default || {}
              const v = p[name]
              return typeof v === 'number' ? v : 0
            })

            const chartData = {
              labels,
              datasets: [{
                data: dataValues,
                backgroundColor: [...colorMap],
                borderWidth: 1,
                borderRadius: 4,
              }],
            }

            return (
              <div key={`${name}-${sortBy}`}>
                <div className="text-xs font-medium text-muted-foreground mb-1">{paramNameLabel(name)}</div>
                <div className="h-48">
                  <Bar
                    key={`${name}-${sortBy}`}
                    data={chartData}
                    options={{
                      responsive: true,
                      maintainAspectRatio: false,
                      animation: false,
                      scales: {
                        x: {
                          grid: { display: false },
                          ticks: { color: 'hsl(215, 20%, 60%)', font: { size: 10 } },
                        },
                        y: {
                          grid: { color: 'hsl(217, 33%, 18%)' },
                          ticks: {
                            color: 'hsl(215, 20%, 60%)',
                            callback: (v) => formatNumber(v as number),
                          },
                          type: isPow2 ? 'logarithmic' as const : 'linear' as const,
                        },
                      },
                      plugins: {
                        legend: { display: false },
                        tooltip: {
                          callbacks: {
                            title: (items) => {
                              const idx = items[0]?.dataIndex
                              const t = sortedTrials[idx]
                              return t ? `Trial #${t.trialIndex}` : ''
                            },
                            label: (ctx) => {
                              const t = sortedTrials[ctx.dataIndex]
                              if (!t) return ''
                              const p = t.params.default || {}
                              const v = p[name]
                              return [
                                `${paramNameLabel(name)}: ${formatNumber(typeof v === 'number' ? v : 0)}`,
                                `Score: ${formatNumber(t.score)}`,
                              ]
                            },
                          },
                        },
                      },
                    }}
                  />
                </div>
              </div>
            )
          })}
        </div>

        {/* Color legend */}
        <div className="flex items-center justify-center gap-2 mt-3">
          <span className="text-xs text-muted-foreground">Low Score</span>
          <div
            className="h-2.5 w-32 rounded-full"
            style={{ background: 'linear-gradient(90deg, hsl(120, 65%, 48%), hsl(0, 65%, 48%))' }}
          />
          <span className="text-xs text-muted-foreground">High Score</span>
        </div>
        <div className="flex items-center justify-center gap-2 mt-1.5">
          <div className="h-2.5 w-6 rounded bg-muted-foreground/40 shrink-0" />
          <span className="text-xs text-muted-foreground">SLA Failed (gray)</span>
        </div>
      </CardContent>
    </Card>
  )
}
