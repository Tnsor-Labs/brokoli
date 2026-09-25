import { useEffect, useId, useMemo, useRef, useState, type KeyboardEvent, type ReactNode } from 'react'
import { createPortal } from 'react-dom'
import { useQuery } from '@tanstack/react-query'
import { useNavigate } from 'react-router-dom'
import { Braces, CornerDownLeft, Keyboard, LogOut, Moon, Plug, Search, Sun, Workflow, type LucideIcon } from 'lucide-react'
import { connectionApi, pipelineApi, variableApi } from '@brokoli/api'
import { useSession } from '@brokoli/auth'
import { keys, paths } from '@brokoli/pipelines'
import { Kbd, cx, errorMessage, useTheme } from '@brokoli/ui'
import { cycle, rank, splitMatch, type ShellAction, type ShellPage } from './commands'
import { openOverlay } from './overlay'

type Kind = 'page' | 'action' | 'pipeline' | 'connection' | 'variable'

type Item = {
  id: string
  kind: Kind
  label: string
  hint?: string
  keywords?: (string | undefined | null)[]
  shortcut?: string
  icon: LucideIcon
  run: (alternate: boolean) => void
}

const GROUPS: { kind: Kind; label: string }[] = [
  { kind: 'page', label: 'Pages' },
  { kind: 'action', label: 'Actions' },
  { kind: 'pipeline', label: 'Pipelines' },
  { kind: 'connection', label: 'Connections' },
  { kind: 'variable', label: 'Variables' },
]
/* Each group is capped separately, so twenty matching pipelines can no longer hide every connection and page. */
const PER_GROUP = 8

function Label({ text, query }: { text: string; query: string }) {
  const parts = splitMatch(text, query)
  if (!parts) return <>{text}</>
  return (
    <>
      {parts[0]}
      <mark>{parts[1]}</mark>
      {parts[2]}
    </>
  )
}

/*
 * Search palette. Pipelines, connections and variables come from the same
 * cached lists the pages use, so anything created or deleted in this session
 * shows up at once; the previous palette loaded them once per page load and
 * never again, and broke outright on multi-tenant servers where the pipeline
 * list is paginated.
 */
export function CommandPalette({ pages, actions, onClose }: { pages: ShellPage[]; actions: ShellAction[]; onClose: () => void }) {
  const navigate = useNavigate()
  const session = useSession()
  const { theme, toggle } = useTheme()
  const [query, setQuery] = useState('')
  const [active, setActive] = useState(0)
  const listId = useId()
  const input = useRef<HTMLInputElement>(null)
  // Cached results show at once; each opening also refetches, so items changed elsewhere (another tab, the API) are current.
  const fresh = { refetchOnMount: 'always' as const }
  const pipelines = useQuery({ queryKey: keys.summary, queryFn: pipelineApi.summary, ...fresh })
  const connections = useQuery({ queryKey: ['connections'], queryFn: connectionApi.list, ...fresh })
  const variables = useQuery({ queryKey: ['variables'], queryFn: variableApi.list, ...fresh })

  useEffect(() => {
    const opener = document.activeElement as HTMLElement | null
    input.current?.focus()
    return () => opener?.focus?.()
  }, [])

  const go = (to: string) => {
    onClose()
    navigate(to)
  }

  const groups = useMemo(() => {
    const canEdit = session.can('pipelines.edit')
    const all: Item[] = [
      ...pages.map(
        (p): Item => ({
          id: `page:${p.to}`,
          kind: 'page',
          label: p.label,
          hint: p.group,
          shortcut: p.key ? `G ${p.key.toUpperCase()}` : undefined,
          icon: CornerDownLeft,
          run: () => go(p.to),
        }),
      ),
      {
        id: 'action:theme',
        kind: 'action',
        label: theme === 'dark' ? 'Switch to light theme' : 'Switch to dark theme',
        keywords: ['theme', 'appearance', 'dark', 'light'],
        icon: theme === 'dark' ? Sun : Moon,
        run: () => {
          toggle()
          onClose()
        },
      },
      { id: 'action:help', kind: 'action', label: 'Keyboard shortcuts', shortcut: '?', keywords: ['keys', 'help'], icon: Keyboard, run: () => openOverlay('help') },
      {
        id: 'action:logout',
        kind: 'action',
        label: 'Sign out',
        keywords: ['log out', 'logout'],
        icon: LogOut,
        run: () => {
          onClose()
          void session.logout()
        },
      },
      ...actions.map(
        (a): Item => ({ id: `action:${a.id}`, kind: 'action', label: a.label, keywords: a.keywords, icon: CornerDownLeft, run: () => go(a.to) }),
      ),
      ...(pipelines.data ?? []).map(
        (p): Item => ({
          id: `pipeline:${p.id}`,
          kind: 'pipeline',
          label: p.name,
          hint: p.draft ? 'Draft' : !p.enabled ? 'Paused' : p.schedule || 'Manual',
          keywords: [p.description, ...(p.tags ?? [])],
          icon: Workflow,
          run: (alternate) => go(alternate && canEdit ? paths.editor(p.id) : paths.runs(p.id)),
        }),
      ),
      ...(connections.data ?? []).map(
        (c): Item => ({
          id: `connection:${c.conn_id}`,
          kind: 'connection',
          label: c.conn_id,
          hint: c.type,
          keywords: [c.type, c.description, c.host],
          icon: Plug,
          run: () => go(`/connections?q=${encodeURIComponent(c.conn_id)}`),
        }),
      ),
      ...(variables.data ?? []).map(
        (v): Item => ({
          id: `variable:${v.key}`,
          kind: 'variable',
          label: v.key,
          hint: v.type,
          keywords: [v.description],
          icon: Braces,
          run: () => go(`/variables?q=${encodeURIComponent(v.key)}`),
        }),
      ),
    ]
    const q = query.trim()
    return GROUPS.map((g) => {
      const own = all.filter((i) => i.kind === g.kind)
      // With nothing typed, offer pages and actions rather than a wall of every pipeline.
      const matches = q ? rank(q, own) : g.kind === 'page' || g.kind === 'action' ? own : []
      return { ...g, items: matches.slice(0, PER_GROUP), more: Math.max(0, matches.length - PER_GROUP) }
    }).filter((g) => g.items.length)
    // `go` and the callbacks only close over stable values.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [pages, actions, query, theme, pipelines.data, connections.data, variables.data])

  const flat = groups.flatMap((g) => g.items)
  const current = flat[Math.min(active, flat.length - 1)]

  useEffect(() => setActive(0), [query])
  useEffect(() => {
    if (current) document.getElementById(`${listId}-${current.id}`)?.scrollIntoView({ block: 'nearest' })
  }, [current, listId])

  const onKey = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
      e.preventDefault()
      setActive((i) => cycle(Math.min(i, flat.length - 1), e.key === 'ArrowDown' ? 1 : -1, flat.length))
    } else if (e.key === 'Enter') {
      e.preventDefault()
      current?.run(e.shiftKey)
    } else if (e.key === 'Escape') {
      e.preventDefault()
      onClose()
    }
  }

  const sources: { label: string; q: { isPending: boolean; isError: boolean; error: unknown } }[] = [
    { label: 'Pipelines', q: pipelines },
    { label: 'Connections', q: connections },
    { label: 'Variables', q: variables },
  ]
  const notes: ReactNode[] = query.trim()
    ? sources.flatMap((s) =>
        s.q.isError ? [<p key={s.label}>{s.label} could not be searched: {errorMessage(s.q.error)}</p>] : s.q.isPending ? [<p key={s.label}>Loading {s.label.toLowerCase()}</p>] : [],
      )
    : []

  return createPortal(
    <div className="sh-backdrop" onMouseDown={(e) => e.target === e.currentTarget && onClose()}>
      <div className="sh-palette" role="dialog" aria-modal="true" aria-label="Search">
        <div className="sh-palette-input">
          <Search size={17} aria-hidden="true" />
          <input
            ref={input}
            role="combobox"
            aria-expanded="true"
            aria-controls={listId}
            aria-autocomplete="list"
            aria-activedescendant={current ? `${listId}-${current.id}` : undefined}
            aria-label="Search pages, pipelines, connections and variables"
            placeholder="Search pages, pipelines, connections and variables"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            onKeyDown={onKey}
            spellCheck={false}
            autoComplete="off"
          />
          <Kbd>Esc</Kbd>
        </div>
        <div className="sh-palette-list" id={listId} role="listbox" aria-label="Results">
          {groups.map((g) => (
            <div key={g.kind} role="group" aria-label={g.label}>
              <div className="sh-group-label" aria-hidden="true">
                {g.label}
                {g.more > 0 && <span>{g.more} more, type more to narrow</span>}
              </div>
              {g.items.map((item) => {
                const Icon = item.icon
                return (
                  <div
                    key={item.id}
                    id={`${listId}-${item.id}`}
                    role="option"
                    aria-selected={item === current}
                    className={cx('sh-option', item === current && 'is-active')}
                    onMouseMove={() => setActive(flat.indexOf(item))}
                    onMouseDown={(e) => e.preventDefault()}
                    onClick={(e) => item.run(e.shiftKey)}
                  >
                    <Icon size={15} aria-hidden="true" />
                    <span className="sh-option-label">
                      <Label text={item.label} query={query} />
                    </span>
                    {item.hint && <span className="sh-option-hint">{item.hint}</span>}
                    {item.shortcut && <Kbd>{item.shortcut}</Kbd>}
                  </div>
                )
              })}
            </div>
          ))}
          {!flat.length && !notes.length && <p className="sh-palette-empty">Nothing matches "{query.trim()}".</p>}
          {notes.length > 0 && <div className="sh-palette-notes">{notes}</div>}
        </div>
        <footer className="sh-palette-foot">
          <span>
            <Kbd>Up</Kbd> <Kbd>Down</Kbd> to move
          </span>
          <span>
            <Kbd>Enter</Kbd> to open
          </span>
          {session.can('pipelines.edit') && (
            <span>
              <Kbd>Shift</Kbd> <Kbd>Enter</Kbd> opens a pipeline in the editor
            </span>
          )}
        </footer>
      </div>
    </div>,
    document.body,
  )
}
