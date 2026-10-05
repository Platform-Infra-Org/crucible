import { useEffect, useState } from 'react'
import { Link, Route, Routes } from 'react-router'
import { MotionConfig } from 'motion/react'
import { api } from './api'
import { CalmContext, MeContext, osCalm, useMe } from './me'
import type { Me } from './types'
import { applyTheme } from './theme/theme'
import { addQuotes } from './lib/quotes'
import { Loader } from './components/Loader'
import { Nav } from './components/Nav'
import { Toaster } from './components/Toaster'
import { Hearth } from './pages/Hearth'
import { TrainingPage } from './pages/Training'
import { ReadingPage } from './pages/Reading'
import { QuizPage } from './pages/Quiz'
import { LabPage } from './pages/Lab'
import { ConnectPage } from './pages/Connect'
import { SettingsPage } from './pages/Settings'
import { TeamPage, TeamsIndex } from './pages/Team'
import { ApprovalsPage } from './pages/Approvals'
import { ForgeStatusPage } from './pages/ForgeStatus'
import { ProgramSettingsPage } from './pages/ProgramSettings'

export { useMe }

export default function App() {
  const [me, setMe] = useState<Me>()
  const [error, setError] = useState<string>()
  useEffect(() => {
    api<Me>('/api/me')
      .then((m) => {
        setMe(m)
        applyTheme(m.user.theme || m.default_theme, m.user.calm_motion)
      })
      .catch((e: Error) => setError(e.message))
    api<{ quotes: string[] }>('/api/meta').then((m) => addQuotes(m.quotes ?? [])).catch(() => {})
  }, [])

  const calm = !!me?.user.calm_motion || osCalm()
  const wrap = (children: React.ReactNode) => (
    <CalmContext.Provider value={calm}>
      <MotionConfig reducedMotion={calm ? 'always' : 'never'} transition={calm ? { duration: 0 } : undefined}>
        {children}
      </MotionConfig>
    </CalmContext.Provider>
  )

  if (error) return wrap(<div className="center"><p className="error">{error}</p></div>)
  if (!me) return wrap(<Loader label="Stoking the forge…" />)

  const setPrefs = async (theme: string, calm: boolean) => {
    await api('/api/me/prefs', { method: 'PUT', json: { theme, calm_motion: calm } })
    setMe({ ...me, user: { ...me.user, theme, calm_motion: calm } })
    applyTheme(theme, calm)
  }
  return wrap(
    <MeContext.Provider value={{ me, setPrefs }}>
        <Nav />
        <Routes>
          <Route path="/" element={<Hearth />} />
          <Route path="/p/:team/:training" element={<TrainingPage />} />
          <Route path="/p/:team/:training/m/:module/read/:item" element={<ReadingPage />} />
          <Route path="/p/:team/:training/m/:module/quiz" element={<QuizPage />} />
          <Route path="/p/:team/:training/m/:module/lab" element={<LabPage />} />
          <Route path="/connect" element={<ConnectPage />} />
          <Route path="/settings" element={<SettingsPage />} />
          <Route path="/teams" element={<TeamsIndex />} />
          <Route path="/teams/:team" element={<TeamPage />} />
          <Route path="/teams/:team/programs/:training" element={<ProgramSettingsPage />} />
          <Route path="/approvals" element={<ApprovalsPage />} />
          <Route path="/admin" element={<ForgeStatusPage />} />
          <Route path="*" element={<div className="center"><div><h1>Lost in the smoke</h1><Link to="/">Back to the Hearth</Link></div></div>} />
        </Routes>
        <Toaster />
    </MeContext.Provider>,
  )
}
