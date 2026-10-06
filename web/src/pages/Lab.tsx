import { useEffect, useMemo, useRef, useState, type PointerEvent as ReactPointerEvent } from 'react'
import { Link, useParams } from 'react-router'
import { motion } from 'motion/react'
import { api, upload } from '../api'
import { useFetch } from '../useFetch'
import type { CheckResult, HintResult, LabView, ModuleLab, TaskDetail } from '../types'
import { usd } from '../lib/money'
import { tierLabel } from './Approvals'
import { clockOffset } from '../lib/timer'
import { askNotifications, notificationsUndecided, toast } from '../lib/alerts'
import { Embers } from '../components/Embers'
import { FeedbackBox } from '../components/Feedback'
import { ErrorBox } from '../components/ErrorBox'
import { IdleModal } from '../components/IdleModal'
import { Loader } from '../components/Loader'
import { Markdown } from '../components/Markdown'
import { SparkBurst } from '../components/SparkBurst'
import { Terminal } from '../components/Terminal'
import { Timer } from '../components/Timer'

const endMessages: Record<string, string> = {
  idle: 'Your lab was closed after a period of inactivity.',
  ttl: 'Time ran out on this lab.',
  user: 'You ended the lab.',
  provision_timeout: 'The lab took too long to start.',
  schedule: "The program's schedule window closed.",
  kill_switch: 'An admin paused all labs.',
  agent_restarted: 'Your laptop agent restarted — start the lab again.',
}

function firstOpen(lab: LabView): string {
  return (lab.tasks.find((t) => t.status === 'open' || t.status === 'setup_failed') ?? lab.tasks[lab.tasks.length - 1]).id
}

export function LabPage() {
  const { team, training, module } = useParams()
  const modPath = `/api/programs/${team}/${training}/modules/${module}/lab`
  const [lab, setLab] = useState<LabView | null>(null)
  // Re-ask while the runtime is not ready, so starting the agent enables "Ignite" without a reload.
  const [poll, setPoll] = useState<number>()
  const { data: info, error } = useFetch<ModuleLab>(modPath, poll)
  const [starting, setStarting] = useState(false)
  const [startErr, setStartErr] = useState<string>()
  useEffect(() => {
    if (!info) return
    setLab(info.lab)
    setPoll(info.runtime_ready ? undefined : 3000)
  }, [info])
  useEffect(() => {
    if (!lab || ['destroyed', 'failed', 'rejected', 'expired'].includes(lab.state)) return
    const id = setInterval(async () => {
      try {
        setLab(await api<LabView>(`/api/labs/${lab.id}`))
      } catch { /* transient; next poll retries */ }
    }, lab.state === 'provisioning' ? 2000 : 5000)
    return () => clearInterval(id)
  }, [lab?.id, lab?.state])

  const back = `/p/${team}/${training}`
  if (error) return <ErrorBox error={error} />
  if (!info) return <Loader label="Opening the workshop…" />
  if (lab?.state === 'provisioning') return <Loader label="Heating the crucible: provisioning your lab…" />
  if (lab && (lab.state === 'ready' || lab.state === 'destroying')) return <LabWorkspace lab={lab} setLab={setLab} title={info.title} back={back} />

  const start = async () => {
    setStarting(true)
    setStartErr(undefined)
    try {
      setLab(await api<LabView>(modPath, { method: 'POST' }))
    } catch (e) {
      setStartErr((e as Error).message)
    } finally {
      setStarting(false)
    }
  }
  const withdraw = async () => {
    if (!lab) return
    try {
      setLab(await api<LabView>(`/api/labs/${lab.id}`, { method: 'DELETE' }))
    } catch (e) {
      setStartErr((e as Error).message)
    }
  }
  const pending = lab?.state === 'pending_approval'
  const cooled = lab?.state === 'destroyed'
  return (
    <section className="page lobby">
      <Embers />
      <Link to={back}>← Back to the training</Link>
      <h1>{info.title}</h1>
      {cooled && lab && (
        <div className="cooled" role="status">
          <h2>The forge has cooled</h2>
          <p>{endMessages[lab.end_reason ?? ''] ?? 'This lab has ended.'} Your progress is saved; passed tasks stay passed.</p>
          <p>
            Score {lab.score} / {lab.max_score} · {lab.tasks.filter((t) => t.status === 'passed').length} of {lab.tasks.length} tasks passed · hints used{' '}
            {lab.tasks.reduce((n, t) => n + t.hints_revealed, 0)}
          </p>
          {lab.tasks.filter((t) => t.review).map((t) => (
            <div key={t.id}><h3>{t.title}</h3><FeedbackBox f={t.review!} /></div>
          ))}
        </div>
      )}
      {lab?.state === 'failed' && <p className="error">The lab failed to start: {lab.error}</p>}
      {pending && lab && (
        <div className="request" role="status" data-testid="request-status">
          <p>
            Waiting for approval from {tierLabel[lab.tier]} · estimated {usd(lab.estimate_usd)}
            {lab.escalate_at && <> · moves up a tier at {new Date(lab.escalate_at).toLocaleString()}</>}
          </p>
          {lab.over_cap && <p className="warn">This would pass a budget cap, so only an admin can approve it.</p>}
          <button className="ghost" onClick={withdraw}>Withdraw request</button>
        </div>
      )}
      {lab?.state === 'rejected' && (
        <p className="error" data-testid="request-status">
          Your request was rejected by {lab.decided_by}{lab.decision_note ? `: “${lab.decision_note}”` : '.'}
        </p>
      )}
      {lab?.state === 'expired' && (
        <p className="muted" data-testid="request-status">
          {lab.end_reason === 'withdrawn' ? 'You withdrew your request.' : 'Nobody answered your request in time.'}
        </p>
      )}
      {!info.runtime_ready && (
        <p className="warn">
          {info.runtime_message} {info.runtime === 'local' && <Link to="/connect">Connect your laptop</Link>}
        </p>
      )}
      {info.blocked && <p className="warn">{info.blocked}</p>}
      {startErr && <p className="error">{startErr}</p>}
      {!pending && (
        <button className="primary big" disabled={!info.runtime_ready || !!info.blocked || starting} onClick={start}>
          {info.needs_approval ? `Request approval (${usd(info.estimate_usd)})` : cooled ? 'Ignite again' : 'Ignite the forge'}
        </button>
      )}
    </section>
  )
}

function LabWorkspace({ lab, setLab, title, back }: { lab: LabView; setLab: (l: LabView) => void; title: string; back: string }) {
  const [split, setSplit] = useState(40)
  const [collapsed, setCollapsed] = useState(false)
  const [fontSize, setFontSize] = useState(() => {
    try { return Math.min(20, Math.max(12, Number(localStorage.getItem('crucible-term-font')) || 14)) } catch { return 14 }
  })
  const bump = (d: number) => setFontSize((f) => {
    const n = Math.min(20, Math.max(12, f + d))
    try { localStorage.setItem('crucible-term-font', String(n)) } catch { /* ignore */ }
    return n
  })
  const [taskId, setTaskId] = useState(() => firstOpen(lab))
  const [active, setActive] = useState(lab.terminals[0].name)
  const [extra, setExtra] = useState<{ key: string; name: string }[]>([])
  const [askAlerts, setAskAlerts] = useState(() => {
    try { return notificationsUndecided() && localStorage.getItem('crucible-alerts') !== 'no' } catch { return notificationsUndecided() }
  })
  const offset = useMemo(() => clockOffset(lab.server_now, Date.now()), [lab.server_now])
  const tabs = [...lab.terminals.map((t) => ({ key: t.name, name: t.name })), ...extra]

  const lastBeat = useRef(0)
  const beat = () => {
    if (Date.now() - lastBeat.current < 60_000) return
    lastBeat.current = Date.now()
    api(`/api/labs/${lab.id}/activity`, { method: 'POST' }).catch(() => {})
  }
  const end = async () => {
    if (!confirm('End this lab? Your progress is kept.')) return
    try {
      setLab(await api<LabView>(`/api/labs/${lab.id}`, { method: 'DELETE' }))
    } catch (e) {
      toast((e as Error).message)
    }
  }
  const extend = async () => {
    try {
      setLab(await api<LabView>(`/api/labs/${lab.id}/extend`, { method: 'POST' }))
    } catch (e) {
      toast((e as Error).message)
    }
  }
  const addShell = () => {
    const name = tabs.find((t) => t.key === active)?.name ?? lab.terminals[0].name
    const key = `${name} #${extra.filter((e) => e.name === name).length + 2}`
    setExtra((x) => [...x, { key, name }])
    setActive(key)
  }
  const startDrag = (e: ReactPointerEvent<HTMLDivElement>) => {
    const body = e.currentTarget.parentElement!
    e.currentTarget.setPointerCapture(e.pointerId)
    const move = (ev: PointerEvent) => {
      const r = body.getBoundingClientRect()
      setSplit(Math.min(70, Math.max(25, ((ev.clientX - r.left) / r.width) * 100)))
    }
    const up = () => {
      window.removeEventListener('pointermove', move)
      window.removeEventListener('pointerup', up)
    }
    window.addEventListener('pointermove', move)
    window.addEventListener('pointerup', up)
  }

  return (
    <div className="lab">
      <header className="lab-head">
        <Link to={back} aria-label="Back to the training">←</Link>
        <h1>{title}</h1>
        <span className="badge">{lab.runtime === 'local' ? 'Your laptop' : lab.runtime}</span>
        {lab.self_reported && <span className="badge warn" title="Checks run on your own machine">self-reported</span>}
        <span className="muted small" title="Scorers can read what your terminals printed">Terminal output is recorded and visible to scorers</span>
        <span className="spacer" />
        <button className="ghost" aria-expanded={!collapsed} onClick={() => setCollapsed((c) => !c)}>{collapsed ? 'Show tasks' : 'Hide tasks'}</button>
        <Timer lab={lab} offset={offset} onExtend={extend} />
        <button className="ghost" onClick={end}>End lab</button>
      </header>
      <div>
        {lab.complete && <div className="banner pass" role="status">✦ Lab forged: {lab.score} / {lab.max_score} points</div>}
        {askAlerts && (
          <div className="banner">
            Get a heads-up when your lab is about to cool down, even from another tab.{' '}
            <button className="ghost" onClick={async () => { await askNotifications(); setAskAlerts(false) }}>Enable alerts</button>
            <button className="ghost" onClick={() => { try { localStorage.setItem('crucible-alerts', 'no') } catch { /* storage blocked */ } setAskAlerts(false) }}>No thanks</button>
          </div>
        )}
      </div>
      <div className="lab-body" style={{ gridTemplateColumns: collapsed ? '0 6px 1fr' : `${split}% 6px 1fr` }}>
        <aside className="tasks" hidden={collapsed} onPointerDown={beat} onKeyDown={beat} onScroll={beat}>
          <ol className="task-pips">
            {lab.tasks.map((t, i) => (
              <li key={t.id}>
                <button className={`pip ${t.status} ${t.id === taskId ? 'current' : ''}`} disabled={t.status === 'locked'} onClick={() => setTaskId(t.id)} aria-label={`Task ${i + 1}: ${t.title} (${t.status})`}>
                  {i + 1}
                </button>
              </li>
            ))}
          </ol>
          <TaskPanel key={taskId} lab={lab} taskId={taskId} onLab={setLab} onAdvance={(l) => setTaskId(firstOpen(l))} />
        </aside>
        <div
          className="splitter" role="separator" aria-orientation="vertical" aria-label="Resize panels" aria-valuenow={Math.round(split)} tabIndex={0}
          onPointerDown={startDrag}
          onKeyDown={(e) => {
            if (e.key === 'ArrowLeft') setSplit((s) => Math.max(25, s - 2))
            if (e.key === 'ArrowRight') setSplit((s) => Math.min(70, s + 2))
          }}
        />
        <section className="terms">
          <div className="tabs" role="tablist" aria-label="Terminals">
            {tabs.map((t) => (
              <button key={t.key} role="tab" aria-selected={active === t.key} onClick={() => setActive(t.key)}>{t.key}</button>
            ))}
            <span className="spacer" />
            <button className="ghost" aria-label="Smaller text" onClick={() => bump(-1)}>A−</button>
            <button className="ghost" aria-label="Larger text" onClick={() => bump(1)}>A+</button>
            <button className="ghost" aria-label="Open another shell" onClick={addShell}>+</button>
          </div>
          {tabs.map((t) => <Terminal key={t.key} labId={lab.id} name={t.name} tabKey={t.key} active={active === t.key} live={lab.state === 'ready'} fontSize={fontSize} />)}
        </section>
      </div>
      <IdleModal lab={lab} offset={offset} onHere={async () => {
        try {
          setLab(await api<LabView>(`/api/labs/${lab.id}/activity`, { method: 'POST' }))
        } catch (e) {
          toast((e as Error).message)
        }
      }} />
    </div>
  )
}

function ReviewForm({ base, onLab, onAdvance }: { base: string; onLab: (l: LabView) => void; onAdvance: (l: LabView) => void }) {
  const [notes, setNotes] = useState('')
  const [files, setFiles] = useState<FileList | null>(null)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState<string>()
  const send = async () => {
    const form = new FormData()
    form.set('answer', notes)
    for (const f of Array.from(files ?? [])) form.append('file', f)
    setBusy(true)
    setErr(undefined)
    try {
      const l = await upload<LabView>(`${base}/submit`, form)
      onLab(l)
      onAdvance(l)
    } catch (e) {
      setErr((e as Error).message)
    } finally {
      setBusy(false)
    }
  }
  return (
    <div className="review-form">
      <label>Notes for the scorer <textarea rows={4} maxLength={20000} value={notes} onChange={(e) => setNotes(e.target.value)} /></label>
      <label>Files for the scorer <input type="file" multiple onChange={(e) => setFiles(e.target.files)} /></label>
      <button className="primary" disabled={busy} onClick={send}>{busy ? 'Submitting…' : 'Submit for review'}</button>
      {err && <p className="error" role="alert">{err}</p>}
    </div>
  )
}

function TaskPanel({ lab, taskId, onLab, onAdvance }: { lab: LabView; taskId: string; onLab: (l: LabView) => void; onAdvance: (l: LabView) => void }) {
  const task = lab.tasks.find((t) => t.id === taskId)!
  const [detail, setDetail] = useState<TaskDetail>()
  const [err, setErr] = useState<string>()
  const [answer, setAnswer] = useState('')
  const [output, setOutput] = useState<{ ok: boolean; text: string }>()
  const [busy, setBusy] = useState(false)
  const [spark, setSpark] = useState(0)
  const [shake, setShake] = useState(0)
  const base = `/api/labs/${lab.id}/tasks/${taskId}`

  useEffect(() => {
    let live = true
    api<TaskDetail>(base)
      .then(async (d) => {
        if (!live) return
        setDetail(d)
        if (d.setup_error) onLab(await api<LabView>(`/api/labs/${lab.id}`))
      })
      .catch((e: Error) => live && setErr(e.message))
    return () => { live = false }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [base])
  const adv = useRef<number>(0)
  useEffect(() => () => clearTimeout(adv.current), [])

  if (err) return <p className="error">{err}</p>
  if (!detail) return <Loader label={task.has_setup && task.status === 'open' ? 'Preparing scenario…' : 'Reading the runes…'} />

  const done = task.status === 'passed' || task.status === 'skipped'
  const check = async () => {
    setBusy(true)
    try {
      const r = await api<CheckResult>(`${base}/check`, { method: 'POST', json: { answer } })
      setOutput({ ok: r.passed, text: r.output || (r.passed ? 'Passed.' : 'Not yet.') })
      onLab(r.lab)
      if (r.passed) {
        setSpark((s) => s + 1)
        adv.current = window.setTimeout(() => onAdvance(r.lab), 1200)
      } else setShake((s) => s + 1)
    } catch (e) {
      setOutput({ ok: false, text: (e as Error).message })
    } finally {
      setBusy(false)
    }
  }
  const hint = async () => {
    const cost = task.next_hint_cost
    if (cost > 0 && !confirm(`This hint costs ${cost} point${cost === 1 ? '' : 's'}. Reveal it?`)) return
    try {
      const r = await api<HintResult>(`${base}/hint`, { method: 'POST' })
      setDetail((d) => d && { ...d, hints: [...(d.hints ?? []), r.text] })
      onLab(r.lab)
    } catch (e) {
      setOutput({ ok: false, text: (e as Error).message })
    }
  }
  const reset = async () => {
    try {
      const l = await api<LabView>(`${base}/reset`, { method: 'POST' })
      onLab(l)
      const failed = l.tasks.find((t) => t.id === taskId)?.status === 'setup_failed'
      setOutput({ ok: !failed, text: failed ? 'Scenario setup failed again.' : 'Scenario reset.' })
    } catch (e) {
      setOutput({ ok: false, text: (e as Error).message })
    }
  }
  const skip = async () => {
    try {
      const l = await api<LabView>(`${base}/skip`, { method: 'POST' })
      onLab(l)
      onAdvance(l)
    } catch (e) {
      setOutput({ ok: false, text: (e as Error).message })
    }
  }

  return (
    <motion.div key={shake} animate={shake ? { x: [0, -8, 8, -5, 5, 0] } : {}} transition={{ duration: 0.4 }}>
      <Markdown text={detail.instructions} />
      {detail.setup_error && <p className="warn">{detail.setup_error}</p>}
      {task.kind === 'quiz' && (
        <label className="quiz-input">
          {task.quiz_prompt}
          <input aria-label="Your answer" value={answer} disabled={done} onChange={(e) => setAnswer(e.target.value)} onKeyDown={(e) => e.key === 'Enter' && !busy && check()} />
        </label>
      )}
      {(detail.hints ?? []).map((h, i) => (
        <div key={i} className="hint">
          <strong>Hint {i + 1}</strong>
          <Markdown text={h} />
        </div>
      ))}
      <div className="row">
        {task.kind !== 'review' && task.status === 'open' && (
          <button className="primary" disabled={busy} onClick={check}>{busy ? 'Checking…' : 'Check'}</button>
        )}
        {task.status === 'open' && task.hints_revealed < task.hints_total && (
          <button className="ghost" onClick={hint}>Hint {task.next_hint_cost > 0 ? `(−${task.next_hint_cost} pts)` : '(free)'}</button>
        )}
        {!done && task.has_setup && <button className="ghost" onClick={reset}>Reset scenario</button>}
        {task.status === 'setup_failed' && <button className="ghost" onClick={skip}>Skip task</button>}
        <SparkBurst trigger={spark} />
      </div>
      {task.kind === 'review' && task.status === 'open' && <ReviewForm base={base} onLab={onLab} onAdvance={onAdvance} />}
      {task.review && <FeedbackBox f={task.review} />}
      {task.status === 'passed' && <p className="pass">✦ Passed: {task.awarded} / {task.points} points</p>}
      {output && <pre className={`check-output ${output.ok ? 'ok' : 'bad'}`} role="status">{output.text}</pre>}
    </motion.div>
  )
}
