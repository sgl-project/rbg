import type { ResultDetail } from '@/types'
import { Card, CardHeader, CardTitle, CardContent } from '@/components/ui/card'
import { formatDate, formatNumber } from '@/lib/utils'
import { Clock, CheckCircle2, XCircle, Zap, Target, BarChart3, Trophy } from 'lucide-react'
import { ConvergenceChart } from './ConvergenceChart'
import { ParamComparison } from './ParamComparison'
import { ParallelCoordinates } from './ParallelCoordinates'

interface OverviewTabProps {
  data: ResultDetail
  templateIndex: number
  multiTemplate?: boolean
}

export function OverviewTab({ data, templateIndex, multiTemplate = false }: OverviewTabProps) {
  const { status, config } = data

  // Best comparison grid for multi-template mode
  const bestPerTemplate = multiTemplate
    ? data.templates
        .filter(t => t.bestTrial)
        .map(t => ({ name: t.name, best: t.bestTrial! }))
    : []

  return (
    <div className="space-y-6 animate-fade-in">
      {/* Status Cards */}
      <div className="grid grid-cols-2 lg:grid-cols-5 gap-3">
        <StatusCard
          icon={<Clock className="h-4 w-4" />}
          label="Duration"
          value={status.duration || 'In Progress...'}
        />
        <StatusCard
          icon={<CheckCircle2 className="h-4 w-4" />}
          label="SLA Pass"
          value={`${status.numSLAPass}/${status.totalTrials}`}
          accent="success"
        />
        <StatusCard
          icon={<XCircle className="h-4 w-4" />}
          label="SLA Fail"
          value={String(status.numSLAFail)}
          accent="destructive"
        />
        <StatusCard
          icon={<Target className="h-4 w-4" />}
          label="Optimize"
          value={config.optimize}
        />
        <StatusCard
          icon={<BarChart3 className="h-4 w-4" />}
          label="Algorithm"
          value={config.algorithm}
        />
      </div>

      {/* Global Best Result */}
      {data.globalBest && (
        <BestResultCard best={data.globalBest} optimize={config.optimize} />
      )}

      {/* Multi-template: best comparison grid */}
      {multiTemplate && bestPerTemplate.length > 1 && (
        <Card>
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-sm">
              <Trophy className="h-4 w-4 text-warning" />
              Best Score per Template
            </CardTitle>
          </CardHeader>
          <CardContent>
            <div className="grid grid-cols-2 md:grid-cols-3 lg:grid-cols-4 gap-3">
              {bestPerTemplate.map(({ name, best }) => {
                const params = best.params.default || {}
                return (
                  <div key={name} className="bg-secondary/30 rounded-md p-3">
                    <div className="flex items-center justify-between mb-2">
                      <span className="text-sm font-medium">{name}</span>
                      <span className="text-xs text-success font-mono font-semibold">
                        {formatNumber(best.score)}
                      </span>
                    </div>
                    <div className="space-y-0.5">
                      {Object.entries(params).map(([k, v]) => (
                        <div key={k} className="flex justify-between text-xs">
                          <span className="text-muted-foreground">{k}</span>
                          <span className="font-mono">{typeof v === 'number' ? formatNumber(v) : String(v)}</span>
                        </div>
                      ))}
                    </div>
                  </div>
                )
              })}
            </div>
          </CardContent>
        </Card>
      )}

      {/* Multi-template: overlay convergence */}
      {multiTemplate ? (
        <ConvergenceChart
          templatesData={data.templates}
          optimize={config.optimize}
          multiTemplate={true}
        />
      ) : (
        <>
          {/* Single-template: convergence */}
          <ConvergenceChart
            trials={data.templates[templateIndex]?.trials || []}
            optimize={config.optimize}
          />

          {/* Parallel Coordinates */}
          <ParallelCoordinates
            trials={data.templates[templateIndex]?.trials || []}
            optimize={config.optimize}
          />

          {/* Parameter Impact */}
          <ParamComparison
            trials={data.templates[templateIndex]?.trials || []}
            optimize={config.optimize}
            searchSpace={data.config.searchSpace.default}
          />
        </>
      )}
    </div>
  )
}

function StatusCard({ icon, label, value, accent }: {
  icon: React.ReactNode
  label: string
  value: string
  accent?: 'success' | 'destructive'
}) {
  const colorClass = accent === 'success' ? 'text-success' : accent === 'destructive' ? 'text-destructive' : 'text-primary'
  return (
    <Card className="p-4">
      <div className="flex items-center gap-2">
        <span className={colorClass}>{icon}</span>
        <span className="text-xs text-muted-foreground uppercase tracking-wider">{label}</span>
      </div>
      <div className="mt-2 text-xl font-semibold font-mono">{value}</div>
    </Card>
  )
}

function BestResultCard({ best, optimize }: { best: NonNullable<ResultDetail['globalBest']>; optimize: string }) {
  const params = best.params.default || {}
  return (
    <Card className="border-success/20 bg-success/5">
      <CardHeader className="pb-2">
        <CardTitle className="flex items-center gap-2 text-sm">
          <span className="inline-flex items-center px-2 py-0.5 rounded text-xs font-medium bg-success/10 text-success border border-success/20">
            BEST TRIAL
          </span>
          Trial #{best.trialIndex} &middot; Score: {formatNumber(best.score)}
        </CardTitle>
      </CardHeader>
      <CardContent>
        <div className="grid grid-cols-3 sm:grid-cols-5 gap-3 text-sm">
          {Object.entries(params).map(([k, v]) => (
            <div key={k}>
              <div className="text-xs text-muted-foreground mb-0.5">{k}</div>
              <div className="font-mono font-medium">{typeof v === 'number' ? formatNumber(v) : String(v)}</div>
            </div>
          ))}
          <div>
            <div className="text-xs text-muted-foreground mb-0.5">{optimize}</div>
            <div className="font-mono font-medium text-success">{formatNumber(best.score)}</div>
          </div>
          {best.metrics && (
            <>
              <div>
                <div className="text-xs text-muted-foreground mb-0.5">TTFT P99</div>
                <div className="font-mono font-medium">{formatNumber(best.metrics.ttftP99)}ms</div>
              </div>
              <div>
                <div className="text-xs text-muted-foreground mb-0.5">TPOT P99</div>
                <div className="font-mono font-medium">{formatNumber(best.metrics.tpotP99)}ms</div>
              </div>
            </>
          )}
        </div>
      </CardContent>
    </Card>
  )
}
