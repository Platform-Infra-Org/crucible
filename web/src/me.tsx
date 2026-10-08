import { createContext, useContext } from 'react'
import type { Me } from './types'

export type MeCtx = { me: Me; setPrefs: (theme: string, calm: boolean) => Promise<void>; setAvatar: (avatar: string) => Promise<void> }
export const MeContext = createContext<MeCtx | null>(null)

export function useMe(): MeCtx {
  const v = useContext(MeContext)
  if (!v) throw new Error('useMe outside <App>')
  return v
}

export const osCalm = () => window.matchMedia('(prefers-reduced-motion: reduce)').matches

// True when the user's setting or the OS asks for no motion.
export const CalmContext = createContext(false)
export const useCalm = () => useContext(CalmContext)
