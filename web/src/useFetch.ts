import { useCallback, useEffect, useState } from 'react'
import { api, ApiError } from './api'

export function useFetch<T>(path: string | null, intervalMs?: number) {
  const [data, setData] = useState<T>()
  const [error, setError] = useState<ApiError>()
  const [tick, setTick] = useState(0)
  useEffect(() => {
    setData(undefined)
    setError(undefined)
  }, [path])
  useEffect(() => {
    if (!path) return
    let live = true
    api<T>(path)
      .then((d) => live && (setData(d), setError(undefined)))
      .catch((e: ApiError) => live && setError(e))
    return () => {
      live = false
    }
  }, [path, tick])
  useEffect(() => {
    if (!intervalMs) return
    const id = setInterval(() => setTick((t) => t + 1), intervalMs)
    return () => clearInterval(id)
  }, [intervalMs])
  const reload = useCallback(() => setTick((t) => t + 1), [])
  return { data, error, reload }
}
