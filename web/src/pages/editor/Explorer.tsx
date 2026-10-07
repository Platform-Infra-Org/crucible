import { Fragment, useEffect, useId, useLayoutEffect, useMemo, useRef, useState } from 'react'
import { pathProblem } from '../../lib/editLimits'
import { ancestors, buildTree, dirOf, iconOf, menuFor, rows as rowsOf, treeKey, type Row } from './tree'
import { Icon } from './icons'

type Props = {
  paths: string[]; greyed: { path: string; reason: string }[]; changed: Set<string>; active: string; readOnly: boolean
  onOpen: (p: string) => void; onNew: (p: string) => void; onRename: (from: string, to: string) => void; onDelete: (p: string) => void
  onNewModule?: () => void
}
type Editing = { kind: 'new'; under: string } | { kind: 'rename'; path: string } // under: the folder the input shows in ('' = top)
type Action = 'new' | 'rename' | 'delete'
const labels: Record<Action, { name: string; kbd?: string; keys?: string }> = { new: { name: 'New file' }, rename: { name: 'Rename', kbd: 'F2', keys: 'F2' }, delete: { name: 'Delete', kbd: 'Del', keys: 'Delete' } }
const depthStyle = (d: number) => ({ '--depth': d }) as React.CSSProperties

// Explorer is the training at the draft's base plus the draft's changes, as a folder tree. Disallowed paths are greyed
// with the reason; training.yaml can be edited but never renamed or deleted. Rename, Delete and New file are on each
// row's context menu (right-click, the ContextMenu key or Shift+F10), F2 and Delete too; names are edited in place.
export function Explorer({ paths, greyed, changed, active, readOnly, onOpen, onNew, onRename, onDelete, onNewModule }: Props) {
  const id = useId()
  const tree = useMemo(() => buildTree(paths, greyed), [paths, greyed])
  const [collapsed, setCollapsed] = useState<Set<string>>(() => new Set())
  const [seen, setSeen] = useState(active)
  if (seen !== active) { // the open file is always in view: its folders open when it changes
    setSeen(active)
    const up = ancestors(active)
    if (up.some((a) => collapsed.has(a))) setCollapsed(new Set([...collapsed].filter((c) => !up.includes(c))))
  }
  const visible = rowsOf(tree, collapsed)
  const [focus, setFocus] = useState('')
  const current = (visible.find((r) => r.node.path === focus) ?? visible.find((r) => r.node.path === active) ?? visible[0])?.node.path
  const rowRefs = useRef(new Map<string, HTMLLIElement>())
  const refocus = useRef<string | undefined>(undefined)
  useEffect(() => {
    const el = refocus.current && rowRefs.current.get(refocus.current)
    if (el) { el.focus(); refocus.current = undefined }
  })
  const [menu, setMenu] = useState<{ x: number; y: number; row: Row } | null>(null)
  const [editing, setEditing] = useState<Editing | null>(null)
  const [value, setValue] = useState('')
  const problem = editing && value ? pathProblem(value) : undefined

  const focusRow = (p: string) => { setFocus(p); rowRefs.current.get(p)?.focus() }
  const toggle = (p: string, close: boolean) => setCollapsed((c) => { const n = new Set(c); if (close) n.add(p); else n.delete(p); return n })
  const startNew = (under: string, dir: string) => {
    if (under) toggle(under, false)
    setEditing({ kind: 'new', under }); setValue(dir || 'modules/')
  }
  const remove = (p: string) => {
    const i = visible.findIndex((r) => r.node.path === p)
    if (!window.confirm(`Delete ${p}?`)) return
    refocus.current = (visible[i + 1] ?? visible[i - 1])?.node.path
    onDelete(p)
  }
  const act = (a: Action, r: Row) => {
    const p = r.node.path
    if (a === 'new') startNew(r.node.kind === 'folder' ? p : r.parent, r.node.kind === 'folder' ? `${p}/` : dirOf(p))
    if (a === 'rename') { setEditing({ kind: 'rename', path: p }); setValue(p) }
    if (a === 'delete') remove(p)
  }
  const done = (commit: boolean) => {
    if (!editing) return
    if (commit) {
      if (problem || !value) return
      if (editing.kind === 'new') onNew(value) // the new file opens in the editor
      else { if (value !== editing.path) onRename(editing.path, value); refocus.current = value }
    } else refocus.current = editing.kind === 'rename' ? editing.path : current
    setEditing(null)
  }
  const openMenu = (r: Row, x: number, y: number) => { setFocus(r.node.path); setMenu({ x, y, row: r }) }
  const onRowKey = (e: React.KeyboardEvent<HTMLLIElement>, r: Row) => {
    const items = menuFor(r.node, readOnly)
    if ((e.key === 'ContextMenu' || (e.shiftKey && e.key === 'F10')) && items.length) {
      e.preventDefault()
      const b = e.currentTarget.getBoundingClientRect()
      return openMenu(r, b.left + 24, b.bottom)
    }
    if (e.key === 'F2' && items.includes('rename')) { e.preventDefault(); return act('rename', r) }
    if (e.key === 'Delete' && items.includes('delete')) { e.preventDefault(); return act('delete', r) }
    const k = treeKey(visible, r.node.path, e.key === ' ' ? 'Enter' : e.key, collapsed)
    if (!k) return
    e.preventDefault()
    if (k.focus) focusRow(k.focus)
    if (k.expand) toggle(k.expand, false)
    if (k.collapse) toggle(k.collapse, true)
    if (k.open) onOpen(k.open)
  }

  const input = (depth: number, label: string) => (
    <li role="none" className="tree-edit" style={depthStyle(depth)}>
      <input autoFocus aria-label={label} value={value} spellCheck={false} aria-invalid={!!problem} aria-describedby={`${id}-problem`}
        onChange={(e) => setValue(e.target.value)} onBlur={() => setEditing(null)}
        onKeyDown={(e) => {
          if (e.key === 'Enter') { e.preventDefault(); done(true) }
          if (e.key === 'Escape') { e.preventDefault(); done(false) }
        }} />
      <span id={`${id}-problem`} role="status" className="tree-problem">{problem}</span>
    </li>
  )
  const newInput = (under: string, depth: number) => editing?.kind === 'new' && editing.under === under && !readOnly && input(depth, 'Path of the new file')
  const allFolders = () => rowsOf(tree, new Set()).flatMap((r) => (r.node.kind === 'folder' ? [r.node.path] : []))

  return (
    <div className="explorer pane" role="region" aria-label="Explorer">
      <div className="pane-head">
        <h2>Explorer</h2>
        {!readOnly && (
          <>
            <button className="icon-btn" aria-label="New file" title="New file" onClick={() => startNew('', active.includes('/') ? dirOf(active) : 'modules/')}><Icon name="new-file" /></button>
            {onNewModule && <button className="icon-btn" aria-label="New module" title="New module" onClick={onNewModule}><Icon name="new-module" /></button>}
          </>
        )}
        <button className="icon-btn" aria-label="Collapse all" title="Collapse all" onClick={() => setCollapsed(new Set(allFolders()))}><Icon name="collapse-all" /></button>
      </div>
      <ul className="tree" role="tree" aria-label="Files">
        {newInput('', 0)}
        {visible.map((r) => {
          const n = r.node
          const p = n.path
          if (editing?.kind === 'rename' && editing.path === p && !readOnly) return <Fragment key={p}>{input(r.depth, `New path for ${p}`)}</Fragment>
          const folder = n.kind === 'folder'
          const open = folder && !collapsed.has(p)
          const greyedReason = n.kind === 'file' ? n.reason : undefined
          const isChanged = changed.has(p)
          const items = menuFor(n, readOnly)
          return (
            <Fragment key={p}>
              <li ref={(el) => { if (el) rowRefs.current.set(p, el); else rowRefs.current.delete(p) }}
                role="treeitem" aria-level={r.depth + 1} aria-expanded={folder ? open : undefined}
                aria-selected={p === active} aria-current={p === active ? 'true' : undefined}
                aria-label={greyedReason !== undefined ? `${p} (can't be edited here: ${greyedReason})` : p}
                aria-describedby={isChanged ? `${id}-changed` : undefined} title={greyedReason}
                tabIndex={p === current ? 0 : -1} style={depthStyle(r.depth)}
                className={`tree-row${greyedReason !== undefined ? ' greyed' : ''}${p === active ? ' active' : ''}`}
                onClick={() => { setFocus(p); if (folder) toggle(p, open); else if (greyedReason === undefined) onOpen(p) }}
                onKeyDown={(e) => onRowKey(e, r)}
                onContextMenu={(e) => { if (!items.length) return; e.preventDefault(); openMenu(r, e.clientX, e.clientY) }}>
                <span className={`twistie${open ? ' open' : ''}`}>{folder && <Icon name="chevron" />}</span>
                <Icon name={folder ? (open ? 'folder-open' : 'folder') : iconOf(p)} className={`ico-${folder ? 'folder' : iconOf(p)}`} />
                <span className="tree-name">{n.name}</span>
                {isChanged && <span className="tree-dot" aria-hidden="true">●</span>}
              </li>
              {folder && open && newInput(p, r.depth + 1)}
            </Fragment>
          )
        })}
      </ul>
      <span id={`${id}-changed`} className="sr-only">(changed)</span>
      {menu && (
        <ContextMenu x={menu.x} y={menu.y} items={menuFor(menu.row.node, readOnly)}
          onPick={(a) => { setMenu(null); act(a, menu.row) }}
          onClose={(restore) => { if (restore) refocus.current = menu.row.node.path; setMenu(null) }} />
      )}
    </div>
  )
}

// ContextMenu is a menu at the pointer (or under the row, from the keyboard). It closes on Escape, Tab, a click
// elsewhere, scrolling or resizing; arrow keys move between its items.
function ContextMenu({ x, y, items, onPick, onClose }: { x: number; y: number; items: Action[]; onPick: (a: Action) => void; onClose: (restore: boolean) => void }) {
  const ref = useRef<HTMLDivElement>(null)
  const [pos, setPos] = useState({ x, y })
  const close = useRef(onClose)
  useEffect(() => { close.current = onClose })
  useLayoutEffect(() => { // stays on screen; the first item takes focus
    const el = ref.current!
    const b = el.getBoundingClientRect()
    setPos({ x: Math.max(4, Math.min(x, window.innerWidth - b.width - 4)), y: Math.max(4, Math.min(y, window.innerHeight - b.height - 4)) })
    el.querySelector<HTMLElement>('[role="menuitem"]')?.focus()
  }, [x, y])
  useEffect(() => {
    const away = (e: Event) => { if (!ref.current?.contains(e.target as Node)) close.current(false) }
    const gone = () => close.current(false)
    document.addEventListener('pointerdown', away, true)
    window.addEventListener('scroll', gone, true)
    window.addEventListener('resize', gone)
    window.addEventListener('blur', gone)
    return () => {
      document.removeEventListener('pointerdown', away, true)
      window.removeEventListener('scroll', gone, true)
      window.removeEventListener('resize', gone)
      window.removeEventListener('blur', gone)
    }
  }, [])
  const onKey = (e: React.KeyboardEvent) => {
    const all = [...ref.current!.querySelectorAll<HTMLElement>('[role="menuitem"]')]
    const i = all.indexOf(document.activeElement as HTMLElement)
    const to = { ArrowDown: i + 1, ArrowUp: i - 1, Home: 0, End: all.length - 1 }[e.key]
    if (to !== undefined) { e.preventDefault(); all[(to + all.length) % all.length]?.focus() }
    if (e.key === 'Escape' || e.key === 'Tab') { e.preventDefault(); onClose(true) }
  }
  return (
    <div ref={ref} role="menu" aria-label="File actions" className="context-menu" style={{ left: pos.x, top: pos.y }} onKeyDown={onKey} onContextMenu={(e) => e.preventDefault()}>
      {items.map((a) => (
        <button key={a} role="menuitem" tabIndex={-1} aria-keyshortcuts={labels[a].keys} onClick={() => onPick(a)}>
          <span>{labels[a].name}</span>{labels[a].kbd && <kbd aria-hidden="true">{labels[a].kbd}</kbd>}
        </button>
      ))}
    </div>
  )
}
