import type { SmithRef } from '../types'

// SmithMark is a person's smith rank beside their name: a hammer whose metal deepens with the rank, and the title in
// words (never colour alone). It never says where anyone stands against anyone else.
export function SmithMark({ smith }: { smith?: SmithRef }) {
  if (!smith) return null
  return (
    <span className={`smith-mark l${smith.level}`}>
      <span aria-hidden="true">⚒</span> {smith.title}
    </span>
  )
}
