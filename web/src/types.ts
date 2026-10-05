export type User = { id: number; email: string; name: string; theme: string; calm_motion: boolean }
export type Me = { user: User; is_admin: boolean; default_theme: string; teams: string[]; can_approve: boolean }
export type TeamSummary = { id: string; name: string; role: string }
export type Roles = { manager: string[]; scorers: string[]; approvers: string[] }
export type LabDefaults = { ttl: string; idle_timeout: string; max_extension: string }
export type ProgramConfig = {
  training: string; title: string; enrolled: string[]; roles: Roles; schedule: string
  lab_defaults: LabDefaults; budget_usd_month: number; can_manage: boolean
}
export type TeamView = {
  id: string; name: string; leader: string; seniors: string[]; members: string[]; trainees: string[]
  mentors: Record<string, string>; budget?: { monthly_usd: number; hard_cap_usd: number }; programs: ProgramConfig[]
  available_trainings: { id: string; title: string }[]; schedules: string[]; platform_sha: string
  can_edit_team: boolean; is_admin: boolean
}
export type ProgramCard = { team: string; team_name: string; training: string; title: string; description: string; percent: number; available: boolean }
export type ItemView = { kind: 'reading' | 'quiz' | 'lab'; id: string; title: string; status: 'new' | 'in_progress' | 'complete' }
export type ModuleView = { id: string; title: string; locked: boolean; complete: boolean; items: ItemView[] }
export type Outline = { team: string; training: string; title: string; description: string; progression: string; percent: number; modules: ModuleView[] }
export type Choice = { id: number; text: string }
export type PublicQuestion = { id: string; type: string; prompt: string; points: number; options?: Choice[]; left?: string[]; right?: Choice[]; human?: boolean }
export type QuizView = { pass_threshold: number; questions: PublicQuestion[]; status: string }
export type QuizResult = { score: number; max: number; percent: number; passed: boolean; correct: Record<string, boolean>; pending_human: boolean }
export type Terminal = { name: string; service: string }
export type TaskStatus = 'locked' | 'open' | 'setup_failed' | 'passed' | 'skipped'
export type TaskView = {
  id: string; title: string; status: TaskStatus; kind: 'check' | 'quiz' | 'review'; points: number; awarded: number
  quiz_prompt?: string; has_setup: boolean; hints_total: number; hints_revealed: number; next_hint_cost: number
}
export type LabState = 'provisioning' | 'ready' | 'destroying' | 'destroyed' | 'failed'
export type LabView = {
  id: string; state: LabState; error?: string; runtime: string; team: string; training: string; module: string
  terminals: Terminal[]; task_order: string; tasks: TaskView[]; server_now: string; ends_at?: string
  limit_reason?: string; end_reason?: string; idle_deadline?: string; idle_warning_s: number
  can_extend: boolean; self_reported: boolean; complete: boolean; score: number; max_score: number
}
export type TaskDetail = TaskView & { instructions: string; hints: string[] | null; setup_error?: string }
export type CheckResult = { passed: boolean; output: string; timed_out: boolean; awarded: number; lab: LabView }
export type HintResult = { index: number; text: string; cost: number; lab: LabView }
export type ModuleLab = { title: string; runtime: string; runtime_ready: boolean; runtime_message?: string; lab: LabView | null }

export type NotificationPrefs = { email_enabled: boolean; kinds: { kind: string; label: string; muted: boolean }[] }
