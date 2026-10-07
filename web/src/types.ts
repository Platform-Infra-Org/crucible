export type User = { id: number; email: string; name: string; theme: string; calm_motion: boolean }
export type Me = { user: User; is_admin: boolean; default_theme: string; teams: string[]; can_approve: boolean; can_score: boolean; can_view_spend: boolean; is_mentor: boolean; can_edit_content: boolean }
export type TeamSummary = { id: string; name: string; role: string }
export type Roles = { manager: string[]; scorers: string[]; approvers: string[] }
export type LabDefaults = { ttl: string; idle_timeout: string; max_extension: string }
export type ProgramConfig = {
  training: string; title: string; enrolled: string[]; roles: Roles; schedule: string; inline_schedule?: string
  lab_defaults: LabDefaults; budget_usd_month: number; review_self_reported: boolean; can_manage: boolean
  running_sha: string; head_sha: string; pinned_ref: string
}
export type Changes = { commits: string[]; stat: string; more?: boolean }
export type TeamView = {
  id: string; name: string; leader: string; seniors: string[]; members: string[]; trainees: string[]
  mentors: Record<string, string>; budget?: { monthly_usd: number; hard_cap_usd: number }; programs: ProgramConfig[]
  available_trainings: { id: string; title: string }[]; schedules: string[]; platform_sha: string
  can_edit_team: boolean; is_admin: boolean
}
export type ProgramCard = { team: string; team_name: string; training: string; title: string; description: string; percent: number; available: boolean }
export type ItemView = { kind: 'reading' | 'quiz' | 'lab'; id: string; title: string; status: 'new' | 'in_progress' | 'pending_review' | 'complete' }
export type ModuleView = { id: string; title: string; locked: boolean; complete: boolean; items: ItemView[] }
export type Outline = { team: string; training: string; title: string; description: string; progression: string; percent: number; modules: ModuleView[] }
export type Choice = { id: number; text: string }
export type PublicQuestion = { id: string; type: string; prompt: string; points: number; options?: Choice[]; left?: string[]; right?: Choice[]; human?: boolean; submission?: Feedback }
export type QuizView = { pass_threshold: number; questions: PublicQuestion[]; status: string; attempts_left?: number; next_attempt_at?: string }
export type QuizResult = { score: number; max: number; percent: number; passed: boolean; correct: Record<string, boolean>; pending_human: boolean; status: 'in_progress' | 'pending_review' | 'complete' }
export type Terminal = { name: string; service: string }
export type TaskStatus = 'locked' | 'open' | 'setup_failed' | 'submitted' | 'passed' | 'skipped'
export type TaskView = {
  id: string; title: string; status: TaskStatus; kind: 'check' | 'quiz' | 'review'; points: number; awarded: number
  quiz_prompt?: string; has_setup: boolean; hints_total: number; hints_revealed: number; next_hint_cost: number; review?: Feedback
}
export type LabState = 'pending_approval' | 'provisioning' | 'ready' | 'destroying' | 'destroyed' | 'failed' | 'rejected' | 'expired'
export type Tier = 'auto' | 'approver' | 'leader' | 'admin'
export type LabView = {
  id: string; state: LabState; error?: string; runtime: string; team: string; training: string; module: string
  terminals: Terminal[]; task_order: string; tasks: TaskView[]; server_now: string; ends_at?: string
  limit_reason?: string; end_reason?: string; idle_deadline?: string; idle_warning_s: number; idle_timeout_s: number
  can_extend: boolean; extension_pending: boolean; self_reported: boolean; lab_review?: Feedback; complete: boolean; score: number; max_score: number
  estimate_usd: number; tier: Tier; over_cap: boolean; escalate_at?: string; decided_by?: string; decision_note?: string
  log?: string[]
}
export type TaskDetail = TaskView & { instructions: string; hints: string[] | null; setup_error?: string }
export type CheckResult = { passed: boolean; output: string; timed_out: boolean; awarded: number; lab: LabView }
export type HintResult = { index: number; text: string; cost: number; lab: LabView }
export type ModuleLab = { title: string; runtime: string; runtime_ready: boolean; runtime_message?: string; lab: LabView | null
  estimate_usd: number; needs_approval: boolean; blocked?: string }

export type NotificationPrefs = { email_enabled: boolean; kinds: { kind: string; label: string; muted: boolean }[] }

export type Spend = { spent_usd: number; committed_usd: number; actual_usd: number; budget_usd: number; cap_usd: number }
export type ScheduleInfo = { name: string; text: string; open: boolean; closes_at?: string; next_open?: string }
export type RecentLab = { module: string; state: LabState; end_reason?: string; estimate_usd: number; created_at: string }
export type Approval = {
  id: string; requester: string; requester_name: string; team: string; training: string; module: string; lab_title: string
  runtime: string; hourly_usd: number; estimate_usd: number; ttl_s: number; tier: Tier; over_cap: boolean
  requested_at: string; escalate_at?: string; team_spend: Spend; program_spend: Spend; recent: RecentLab[]; schedule: ScheduleInfo
  kind: 'request' | 'extension'; extend_until?: string
}
export type KillSwitch = { enabled: boolean; changed_by?: string; changed_at?: string }
export type AuditEntry = { at: string; actor: string; action: string; target: string; detail: Record<string, unknown>; commit_sha?: string }
export type TrainingStatus = { id: string; repo: string; branch: string; head: string; problems: string[] }
export type PlatformView = {
  platform_sha: string; platform_error?: string; synced_at: string
  cost_tiers: { auto_approve_usd: number; tier1_usd: number; tier2_usd: number } | null
  escalation_hours: number; schedules: Record<string, string>; admins: string[]; trainings: TrainingStatus[]; audit: AuditEntry[]
  pending_edits: number; attention: AttentionLab[]; programs: ProgramPin[]
}
export type AttentionLab = { id: string; trainee: string; team: string; training: string; module: string; state: string; error?: string; since: string }
export type ProgramPin = { team: string; training: string; running: string; head: string; pinned_ref?: string }

export type SubmissionType = 'text' | 'upload' | 'signoff' | 'review' | 'self_reported'
export type SubmissionFile = { name: string; size: number }
export type Feedback = {
  id: number; status: 'pending' | 'scored' | 'returned'; answer: string; files: SubmissionFile[] | null
  points: number; max_points: number; feedback: string; scored_by?: string
}
export type Submission = {
  id: number; trainee: string; trainee_name: string; team: string; training: string; module: string
  kind: 'question' | 'task' | 'lab'; item: string; lab_id?: string; type: SubmissionType; prompt: string; rubric: string
  max_points: number; answer: string; files: SubmissionFile[]; status: Feedback['status']; points: number
  feedback: string; scored_by?: string; scored_at?: string; created_at: string
}
export type CheckRun = { lab_id: string; at: string; exit_code: number; output: string; answer?: string; self_reported: boolean }
export type TaskEvidence = {
  id: string; title: string; kind: 'check' | 'quiz' | 'review'; status: string; points: number; awarded: number
  hints_used: number; hint_cost: number; checks: CheckRun[]
}
export type TranscriptInfo = { id: number; lab_id: string; terminal: string; bytes: number; truncated: boolean; started_at: string; ended_at: string }
export type LabEvidence = { runtime: string; self_reported: boolean; tasks: TaskEvidence[]; transcripts: TranscriptInfo[] }
export type AnvilDetail = { submission: Submission; history: Submission[]; lab?: LabEvidence }
export type SignOff = { team: string; training: string; module: string; question: string; prompt: string; points: number; trainee: string; trainee_name: string }

export type LedgerLab = {
  id: string; team: string; training: string; module: string; requester: string; runtime: string; state: LabState
  hourly_usd: number; estimate_usd: number; cost_usd: number; actual_usd?: number; settled: boolean; created_at: string; ends_at?: string
}
export type Finding = { source: 'destroy' | 'reaper' | 'trail'; lab_id: string; arn: string; action: 'deleted' | 'failed' | 'reported'; detail: string; first_at: string; last_at: string }
export type Accuracy = { labs: number; estimate_usd: number; actual_usd: number }
export type Ledger = {
  month: string; aws: boolean; actuals_as_of?: string; actuals_stale: boolean; reaper_stale: boolean; reaper_error?: string; actuals_error?: string; reaped_at?: string; can_refresh: boolean
  teams: { id: string; name: string; spend: Spend; programs: { training: string; spend: Spend }[] }[]
  daily: { day: string; estimate_usd: number; actual_usd: number }[]
  running: LedgerLab[]; labs: LedgerLab[]; top_spenders: { requester: string; team: string; usd: number }[]
  accuracy: Accuracy; findings?: Finding[]
}

export type Forge = { percent: number; level: number; rank: string; ladder: { name: string; at: number }[]; badges: { training: string; title: string; earned_at: string }[]; rank_up: boolean }
export type Heat = 'cold' | 'glowing' | 'forged'
export type JourneyCell = { module: string; title: string; heat: Heat }
export type JourneyFlag = { kind: 'failed_checks' | 'final_hint' | 'inactive' | 'returned_twice' | 'not_started'; module?: string; item?: string; detail: string; at?: string }
export type JourneyRow = { email: string; name: string; team: string; training: string; title: string; percent: number; cells: JourneyCell[]; flags: JourneyFlag[]; last_active?: string }
export type Mentee = {
  email: string; name: string; team: string; rank: string; programs: JourneyRow[]
  pending: { id: number; training: string; module: string; item: string; type: string; created_at: string }[]
  failures: { training: string; module: string; task?: string; kind: 'lab_failed' | 'check_failed'; detail: string; at: string }[]
}
export type EditOp = { op: 'put'; path: string; content: string } | { op: 'rename'; from: string; to: string } | { op: 'delete'; path: string }
export type FileEntry = { path: string; size: number; editable: boolean; reason?: string }
export type EditStatus = 'pending' | 'merged' | 'rejected' | 'withdrawn' | 'stale'
export type ContentEdit = {
  id: number; training: string; title: string; author: string; base_sha: string; head_sha?: string; branch: string; status: EditStatus
  reviewer?: string; note?: string; merge_sha?: string; created_at: string; decided_at?: string; ops?: EditOp[]; diff?: string
  can_review: boolean; can_withdraw: boolean
}

export type CatalogEntry = { id: string; title: string; description: string; estimated_hours: number; modules: number; enrolled: { id: string; name: string }[]; available: boolean }
export type MyLab = { id: string; team: string; training: string; module: string; title: string; runtime: string; state: LabState; created_at: string; ends_at?: string; link: string }

export type DraftState = 'editing' | 'in_review' | 'returned' | 'merged'
export type DraftInfo = { id: number; training: string; title: string; base_sha: string; head_sha: string; ops: EditOp[]; updated_at: string; edit_id?: number; state: DraftState }
export type Conflict = { path: string; base: string; head: string; head_missing: boolean; mine: string; op: EditOp }
export type Problem = { file: string; line: number; msg: string }
export type BlockField = { name: string; type: string; required?: boolean; enum?: string[]; default?: string; description: string; min?: number; max?: number }
export type BlockInfo = { id: string; group: string; title: string; summary: string; doc?: string; file_kind: string; fields: BlockField[]; example: string; git_only?: boolean }
