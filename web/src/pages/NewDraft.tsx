import { useEffect, useRef, useState } from 'react'
import { useNavigate, useSearchParams } from 'react-router'
import { api, type ApiError } from '../api'
import type { DraftInfo } from '../types'
import { ErrorBox } from '../components/ErrorBox'
import { Loader } from '../components/Loader'

// NewDraftPage creates a draft (optionally reopening a returned edit with all its ops) and replaces itself with the editor.
export function NewDraftPage() {
  const [q] = useSearchParams()
  const nav = useNavigate()
  const [err, setErr] = useState<ApiError>()
  const started = useRef(false) // StrictMode runs effects twice in dev: one draft, not two
  useEffect(() => {
    if (started.current) return
    started.current = true
    const from = Number(q.get('from') ?? 0)
    api<DraftInfo>('/api/authoring/drafts', { method: 'POST', json: { training: q.get('training') ?? '', title: '', ...(from ? { from_edit: from } : {}) } })
      .then((d) => nav(`/edits/drafts/${d.id}`, { replace: true }))
      .catch(setErr)
  }, [q, nav])
  return err ? <ErrorBox error={err} /> : <Loader label="Laying out a fresh draft…" />
}
