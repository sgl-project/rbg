import { useMemo } from 'react'
import { Line } from 'react-chartjs-2'
import {
  Chart as ChartJS,
  CategoryScale,
  LinearScale,
  PointElement,
  LineElement,
  Title,
  Tooltip,
  Filler,
  Legend,
} from 'chart.js'
import type { TrialResult, TemplateDetail } from '@/types'
import { Card, CardHeader, CardTitle, CardContent } from '@/components/ui/card'
import { formatNumber, isSlaPass } from '@/lib/utils'

ChartJS.register(CategoryScale, LinearScale, PointElement, LineElement, Title, Tooltip, Filler, Legend)

interface ConvergenceChartProps {
  trials?: TrialResult[]
  optimize: string
  multiTemplate?: boolean
  templatesData?: TemplateDetail[]
}

const templateColors = [
  'hsl(217, 91%, 60%)',
  'hsl(142, 71%, 45%)',
  'hsl(30, 95%, 55%)',
  'hsl(280, 65%, 55%)',
  'hsl(340, 75%, 55%)',
  'hsl(50, 85%, 50%)',
]

export function ConvergenceChart({ trials, optimize, multiTemplate, templatesData }: ConvergenceChartProps) {
  const data = useMemo(() => {
    if (multiTemplate && templatesData) {
      // Multi-template: one dataset per template
      const maxLen = Math.max(...templatesData.map(t => t.trials.length))
      const labels = Array.from({ length: maxLen }, (_, i) => `#${i}`)

      const datasets = templatesData.map((tmpl, idx) => {
        const color = templateColors[idx % templateColors.length]
        const sortedTrials = [...tmpl.trials].sort((a, b) => a.trialIndex - b.trialIndex)

        let best = 0
        const passPoints: (number | null)[] = []
        const bestSoFar: number[] = []

        for (const t of sortedTrials) {
          const pass = isSlaPass(t.constraints)
          if (pass && t.score > best) best = t.score
          bestSoFar.push(best)
          passPoints.push(pass ? t.score : null)
        }

        return {
          label: `${tmpl.name}`,
          data: Array.from({ length: maxLen }, (_, i) => (i < passPoints.length ? passPoints[i] : null)),
          borderColor: color,
          backgroundColor: color,
          tension: 0.3,
          pointRadius: 4,
          pointHoverRadius: 6,
          spanGaps: true,
          borderWidth: 2,
        }
      })

      return { labels, datasets }
    }

    // Single-template mode
    const trialList = trials || []
    const labels = trialList.map((_, i) => `#${i}`)
    const passMarks = trialList.map(t => isSlaPass(t.constraints) ? t.score : NaN)

    const bestSoFar: number[] = []
    let best = 0
    for (const t of trialList) {
      if (isSlaPass(t.constraints) && t.score > best) best = t.score
      bestSoFar.push(best)
    }

    return {
      labels,
      datasets: [
        {
          label: 'Best Score',
          data: bestSoFar,
          borderColor: 'hsl(142, 71%, 45%)',
          backgroundColor: 'hsl(142, 71%, 45%, 0.1)',
          fill: false,
          tension: 0.2,
          pointRadius: 0,
          borderWidth: 2,
          borderDash: [6, 3],
        },
        {
          label: 'Score (SLA pass)',
          data: passMarks,
          borderColor: 'hsl(217, 91%, 60%)',
          backgroundColor: 'hsl(217, 91%, 60%)',
          tension: 0.3,
          pointRadius: 5,
          pointHoverRadius: 7,
          spanGaps: true,
        },
      ],
    }
  }, [trials, optimize, multiTemplate, templatesData])

  return (
    <Card>
      <CardHeader>
        <CardTitle>Optimization Convergence</CardTitle>
      </CardHeader>
      <CardContent>
        <div className="h-72">
          <Line
            data={data}
            options={{
              responsive: true,
              maintainAspectRatio: false,
              interaction: { intersect: false, mode: 'index' },
              scales: {
                x: { grid: { color: 'hsl(217, 33%, 18%)' }, ticks: { color: 'hsl(215, 20%, 60%)' } },
                y: {
                  grid: { color: 'hsl(217, 33%, 18%)' },
                  ticks: { color: 'hsl(215, 20%, 60%)', callback: (v) => formatNumber(v as number) },
                  title: { display: true, text: optimize, color: 'hsl(215, 20%, 60%)' },
                },
              },
              plugins: {
                legend: { labels: { color: 'hsl(215, 20%, 60%)', usePointStyle: true, padding: 20 } },
                tooltip: {
                  callbacks: {
                    label: (ctx) => {
                      const v = ctx.parsed.y
                      if (v == null) return ''
                      return `${ctx.dataset.label}: ${formatNumber(v)}`
                    },
                  },
                },
              },
            }}
          />
        </div>
      </CardContent>
    </Card>
  )
}
