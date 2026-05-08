export interface Metrics {
  ttftP50: number
  ttftP99: number
  tpotP50: number
  tpotP99: number
  outputThroughput: number
  inputThroughput: number
  totalThroughput: number
  requestsPerSecond: number
  errorRate: number
  numCompletedRequests: number
  numErrorRequests: number
  numRequests: number
}

export interface ParamSet {
  [key: string]: number | string | boolean
}

export interface RoleParamSet {
  [role: string]: ParamSet
}

export interface TrialResult {
  trialIndex: number
  templateName: string
  params: RoleParamSet
  metrics: Metrics | null
  constraints: number[] | null
  slaPass?: boolean  // derived from constraints: true if all constraints <= 0
  score: number
  error: string
  duration: string
  startTime: string
  endTime: string
}

export interface ResultStatus {
  startTime: string
  endTime?: string
  duration?: string
  numTemplates: number
  totalTrials: number
  numSLAPass: number
  numSLAFail: number
}

export interface ResultSLA {
  ttftP99MaxMs?: number
  tpotP99MaxMs?: number
  errorRateMax?: number
}

export interface SearchParamDetail {
  type: string
  values?: (number | string)[]
  min?: number
  max?: number
  step?: number
}

export interface SearchSpace {
  [role: string]: Record<string, SearchParamDetail>
}

export interface ResultConfig {
  name: string
  backend: string
  algorithm: string
  optimize: string
  sla: ResultSLA
  scenarioName: string
  scenarioWorkloads: string[]
  scenarioConcurrency: number[]
  maxTrialsPerTemplate: number
  earlyStopPatience: number
  timeout: string
  templates: string[]
  searchSpace: SearchSpace
}

export interface TemplateDetail {
  name: string
  bestTrial: TrialResult | null
  trials: TrialResult[]
}

export interface ResultDetail {
  experimentId: string
  status: ResultStatus
  config: ResultConfig
  templates: TemplateDetail[]
  globalBest: TrialResult | null
}
