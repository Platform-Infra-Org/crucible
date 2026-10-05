import { useMemo } from 'react'

export function Embers({ count = 18 }: { count?: number }) {
  const embers = useMemo(
    () => Array.from({ length: count }, (_, i) => ({ i, left: Math.random() * 100, delay: Math.random() * 6, dur: 4 + Math.random() * 4, size: 2 + Math.random() * 3 })),
    [count],
  )
  return (
    <div className="embers" aria-hidden="true">
      {embers.map((e) => (
        <span key={e.i} style={{ left: `${e.left}%`, animationDelay: `${e.delay}s`, animationDuration: `${e.dur}s`, width: e.size, height: e.size }} />
      ))}
    </div>
  )
}
