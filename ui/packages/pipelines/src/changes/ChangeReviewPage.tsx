import { useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link, useNavigate } from 'react-router-dom'
import ReactMarkdown from 'react-markdown'
import { Bot, FlaskConical, GitPullRequestArrow, Minus, Plus, User } from 'lucide-react'
import {
  pipelineApi,
  type ChangeRequest,
  type FieldChange,
  type Pipeline,
  type PipelineNode,
  type PipelinePreview,
} from '@brokoli/api'
import { useSession } from '@brokoli/auth'
import { PipelineGraph } from '@brokoli/pipeline-session'
import {
  Badge,
  Button,
  Callout,
  ConfirmDialog,
  EmptyState,
  Page,
  PageHeader,
  Skeleton,
  StatusBadge,
  Tabs,
  Textarea,
  cx,
  errorMessage,
  formatDateTime,
  formatRelative,
  useToast,
} from '@brokoli/ui'
import { keys, paths } from '../keys'
import { cannotApply, changeAuthor } from '../runs/changes'
import {
  codeLanguage,
  compareTest,
  diffCounts,
  fieldKind,
  fieldLabel,
  graphDiff,
  hunks,
  lineDiff,
  showValue,
  type Mark,
  type NodeComparison,
} from './review'
import './changes.css'

/*
 * One change request, reviewed (ADR-046): what it says, what it changes
 * node by node, the graph it makes, and a test of it on a few rows, before
 * a person applies or declines it.
 */

type Tab = 'overview' | 'changes' | 'graph' | 'test'

export function ChangeReviewPage({
  pipelineId,
  changeId,
}: {
  pipelineId: string
  changeId: string
}) {
  const navigate = useNavigate()
  const pipeline = useQuery({
    queryKey: keys.pipeline(pipelineId),
    queryFn: () => pipelineApi.get(pipelineId),
  })
  const change = useQuery({
    queryKey: [...keys.pipeline(pipelineId), 'changes', changeId],
    queryFn: () => pipelineApi.change(pipelineId, changeId),
  })
  const [tab, setTab] = useState<Tab>('overview')
  const p = pipeline.data
  const cr = change.data
  const counts = diffCounts(cr?.diff)
  const changedCount =
    counts.added + counts.changed + counts.removed + counts.settings + (counts.edges ? 1 : 0)

  if (change.isError)
    return (
      <Page>
        <Callout
          tone="danger"
          title="This change request could not be loaded"
          action={
            <Button onClick={() => navigate(paths.runs(pipelineId))}>Back to the pipeline</Button>
          }
        >
          {errorMessage(change.error)}
        </Callout>
      </Page>
    )

  return (
    <Page wide className="bk-cr-page">
      <nav className="bk-crumbs" aria-label="Breadcrumb">
        <Link to={paths.list}>Pipelines</Link>
        <span aria-hidden="true">/</span>
        <Link to={paths.runs(pipelineId)}>{p?.name ?? 'Pipeline'}</Link>
        <span aria-hidden="true">/</span>
        <span aria-current="page">Proposed change</span>
      </nav>
      <PageHeader
        eyebrow="Proposed change"
        title={cr?.title ?? <Skeleton width={360} height={32} />}
        meta={cr && <ChangeMeta cr={cr} />}
        actions={cr && p && <Decision cr={cr} pipeline={p} />}
      />
      <Tabs<Tab>
        label="Review"
        value={tab}
        onChange={setTab}
        items={[
          { id: 'overview', label: 'Overview' },
          { id: 'changes', label: 'Changes', count: changedCount || undefined },
          { id: 'graph', label: 'Graph' },
          { id: 'test', label: 'Test' },
        ]}
      />
      {!cr || !p ? (
        <Skeleton height={240} />
      ) : tab === 'overview' ? (
        <Overview cr={cr} />
      ) : tab === 'changes' ? (
        <Changes cr={cr} current={p} />
      ) : tab === 'graph' ? (
        <GraphTab cr={cr} current={p} />
      ) : (
        <TestTab cr={cr} pipelineId={pipelineId} />
      )}
    </Page>
  )
}

function ChangeMeta({ cr }: { cr: ChangeRequest }) {
  const Icon = cr.created_by_kind === 'agent' ? Bot : User
  return (
    <>
      <span className="bk-cr-author">
        <Icon size={14} aria-hidden="true" /> {changeAuthor(cr)} · {formatRelative(cr.created_at)}
      </span>
      {cr.status !== 'open' && (
        <Badge tone={cr.status === 'applied' ? 'success' : 'neutral'}>{cr.status}</Badge>
      )}
      {cr.stale && <Badge tone="warning">Out of date</Badge>}
      {cr.validation.valid ? (
        <Badge tone="success">Valid</Badge>
      ) : (
        <Badge tone="danger">Not valid</Badge>
      )}
      {cr.delivery?.url && (
        <a className="bk-cr-delivery" href={cr.delivery.url} target="_blank" rel="noreferrer">
          <GitPullRequestArrow size={13} aria-hidden="true" /> Pull request
        </a>
      )}
    </>
  )
}

// ── Apply / Decline ──

function Decision({ cr, pipeline }: { cr: ChangeRequest; pipeline: Pipeline }) {
  const session = useSession()
  const toast = useToast()
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const [confirm, setConfirm] = useState<null | 'apply' | 'decline'>(null)
  const [reason, setReason] = useState('')
  if (cr.status !== 'open') return null
  const blocked = cannotApply(cr, {
    canEdit: session.can('pipelines.edit'),
    fromGit: pipeline.source === 'git',
  })
  const mine = session.user?.id === cr.created_by
  const mayDecline = session.can('pipelines.edit') || mine
  const refresh = () => {
    void queryClient.invalidateQueries({ queryKey: keys.pipeline(cr.pipeline_id) })
    void queryClient.invalidateQueries({ queryKey: keys.summary })
  }
  return (
    <>
      {mayDecline && (
        <Button onClick={() => setConfirm('decline')}>{mine ? 'Withdraw' : 'Decline'}</Button>
      )}
      <Button
        variant="primary"
        disabled={Boolean(blocked)}
        title={blocked ?? 'Apply this change'}
        onClick={() => setConfirm('apply')}
      >
        Apply
      </Button>
      {confirm === 'apply' && (
        <ConfirmDialog
          title={`Apply "${cr.title}"?`}
          confirmLabel="Apply"
          onCancel={() => setConfirm(null)}
          onConfirm={async () => {
            try {
              const res = await pipelineApi.applyChange(cr.pipeline_id, cr.id)
              toast.success('Change applied', `The pipeline is now version ${res.version}.`)
              refresh()
              navigate(paths.runs(cr.pipeline_id))
            } catch (e) {
              toast.error('Not applied', e)
            }
            setConfirm(null)
          }}
        >
          The pipeline becomes the proposed version, as if it were saved from the editor. The
          previous version stays in its history.
        </ConfirmDialog>
      )}
      {confirm === 'decline' && (
        <ConfirmDialog
          title={`${mine ? 'Withdraw' : 'Decline'} "${cr.title}"?`}
          confirmLabel={mine ? 'Withdraw' : 'Decline'}
          tone="danger"
          onCancel={() => setConfirm(null)}
          onConfirm={async () => {
            try {
              await pipelineApi.declineChange(cr.pipeline_id, cr.id, reason)
              toast.success(mine ? 'Change withdrawn' : 'Change declined')
              refresh()
              navigate(paths.runs(cr.pipeline_id))
            } catch (e) {
              toast.error('Not declined', e)
            }
            setConfirm(null)
          }}
        >
          <Textarea
            aria-label="Reason (optional)"
            placeholder="Why, for whoever proposed it (optional)"
            value={reason}
            onChange={(e) => setReason(e.target.value)}
          />
        </ConfirmDialog>
      )}
    </>
  )
}

// ── Overview ──

function Overview({ cr }: { cr: ChangeRequest }) {
  const agent = cr.created_by_kind === 'agent'
  const Icon = agent ? Bot : User
  return (
    <div className="bk-cr-overview">
      <div className="bk-cr-main">
        {!cr.validation.valid && (
          <Callout tone="danger" title="The proposed pipeline is not valid">
            <ul>
              {(cr.validation.errors ?? []).map((e, i) => (
                <li key={i}>{e}</li>
              ))}
            </ul>
          </Callout>
        )}
        {cr.stale && (
          <Callout tone="warning" title="The pipeline changed since this was proposed">
            Applying it would undo those changes. Decline it and ask for a new proposal based on the
            current version.
          </Callout>
        )}
        {cr.reason && (
          <Callout
            tone="info"
            title={`${cr.status === 'applied' ? 'Applied' : 'Declined'} by ${cr.decided_by_name ?? 'someone'}`}
          >
            {cr.reason}
          </Callout>
        )}
        <article className="bk-cr-doc" aria-label="Description">
          <header className="bk-cr-doc-head">
            <span className={cx('bk-cr-avatar', agent && 'is-agent')} aria-hidden="true">
              <Icon size={16} />
            </span>
            <span className="bk-cr-doc-who">
              <strong>{cr.created_by_name || 'someone'}</strong>
              {agent && <span className="bk-cr-tag">Agent</span>}
              {agent && cr.on_behalf_of && (
                <span className="bk-cr-doc-for">on behalf of {cr.on_behalf_of}</span>
              )}
            </span>
            <time
              className="bk-cr-doc-time"
              dateTime={cr.created_at}
              title={formatDateTime(cr.created_at)}
            >
              proposed {formatRelative(cr.created_at)}
            </time>
          </header>
          <div className="bk-cr-markdown">
            {cr.description ? (
              <ReactMarkdown>{cr.description}</ReactMarkdown>
            ) : (
              <p className="bk-muted">
                No description was given. The Changes and Test tabs show what it does.
              </p>
            )}
          </div>
        </article>
      </div>
      <Details cr={cr} />
    </div>
  )
}

function Details({ cr }: { cr: ChangeRequest }) {
  const c = diffCounts(cr.diff)
  const touched = [
    c.added && `${c.added} added`,
    c.changed && `${c.changed} changed`,
    c.removed && `${c.removed} removed`,
  ].filter(Boolean) as string[]
  return (
    <aside className="bk-cr-details" aria-label="Details">
      <dl>
        <div>
          <dt>Status</dt>
          <dd>
            {cr.status === 'open'
              ? cr.stale
                ? 'Open, out of date'
                : 'Open, awaiting a decision'
              : cr.status === 'applied'
                ? `Applied as version ${cr.applied_version ?? ''}`
                : 'Declined'}
          </dd>
        </div>
        <div>
          <dt>Proposed by</dt>
          <dd>{changeAuthor(cr)}</dd>
        </div>
        <div>
          <dt>Against</dt>
          <dd>version {cr.base_version}</dd>
        </div>
        <div>
          <dt>Validation</dt>
          <dd className={cr.validation.valid ? 'is-ok' : 'is-bad'}>
            {cr.validation.valid ? 'Passes' : `${(cr.validation.errors ?? []).length} error(s)`}
          </dd>
        </div>
        <div>
          <dt>Steps</dt>
          <dd>{touched.length ? touched.join(', ') : 'none'}</dd>
        </div>
        {c.edges > 0 && (
          <div>
            <dt>Connections</dt>
            <dd>{c.edges} rewired</dd>
          </div>
        )}
        {c.settings > 0 && (
          <div>
            <dt>Settings</dt>
            <dd>{(cr.diff?.pipeline ?? []).map((f) => f.field).join(', ')}</dd>
          </div>
        )}
        {cr.delivery?.url && (
          <div>
            <dt>Pull request</dt>
            <dd>
              <a href={cr.delivery.url} target="_blank" rel="noreferrer">
                {cr.delivery.ref || 'Open'}
              </a>
            </dd>
          </div>
        )}
      </dl>
      <p className="bk-cr-details-note">
        Nothing changes until someone who may edit this pipeline applies it. Applying is a save: the
        previous version stays in the history.
      </p>
    </aside>
  )
}

// ── Changes ──

function Changes({ cr, current }: { cr: ChangeRequest; current: Pipeline }) {
  const diff = cr.diff
  const proposed = cr.proposed
  const names = useMemo(() => {
    const m = new Map<string, string>()
    for (const n of [...(current.nodes ?? []), ...(proposed?.nodes ?? [])])
      m.set(n.id, n.name || n.id)
    return m
  }, [current, proposed])
  const nodeOf = (list: PipelineNode[] | undefined, id: string) => list?.find((n) => n.id === id)
  if (!diff) return <Skeleton height={200} />
  const edges = [
    ...(diff.edges_removed ?? []).map((e) => ({ e, mark: 'removed' as const })),
    ...(diff.edges_added ?? []).map((e) => ({ e, mark: 'added' as const })),
  ]
  return (
    <div className="bk-cr-changes">
      {(diff.pipeline ?? []).length > 0 && (
        <NodeSection title="Pipeline settings" mark="changed">
          {(diff.pipeline ?? []).map((c) => (
            <FieldDiff key={c.field} change={c} nodeType="" />
          ))}
        </NodeSection>
      )}
      {(diff.nodes_changed ?? []).map((n) => (
        <NodeSection key={n.id} title={n.name} type={n.type} mark="changed">
          {n.changes.map((c) => (
            <FieldDiff
              key={c.field}
              change={c}
              nodeType={n.type}
              node={nodeOf(proposed?.nodes, n.id)}
            />
          ))}
        </NodeSection>
      ))}
      {(diff.nodes_added ?? []).map((n) => {
        const node = nodeOf(proposed?.nodes, n.id)
        return (
          <NodeSection key={n.id} title={n.name} type={n.type} mark="added">
            {Object.entries(node?.config ?? {})
              .filter(([k]) => !k.startsWith('_'))
              .map(([k, v]) => (
                <FieldDiff
                  key={k}
                  change={{ field: `config.${k}`, after: v }}
                  nodeType={n.type}
                  node={node}
                />
              ))}
          </NodeSection>
        )
      })}
      {(diff.nodes_removed ?? []).map((n) => (
        <NodeSection key={n.id} title={n.name} type={n.type} mark="removed">
          <p className="bk-muted">This node and its settings are removed.</p>
        </NodeSection>
      ))}
      {edges.length > 0 && (
        <NodeSection title="Connections" mark="changed">
          <ul className="bk-cr-edges">
            {edges.map(({ e, mark }) => (
              <li key={`${mark}${e.from}${e.to}`} className={`is-${mark}`}>
                {mark === 'added' ? (
                  <Plus size={13} aria-label="Connects" />
                ) : (
                  <Minus size={13} aria-label="Disconnects" />
                )}
                <span>{names.get(e.from) ?? e.from}</span>
                <span aria-hidden="true">→</span>
                <span>{names.get(e.to) ?? e.to}</span>
                {e.condition !== undefined && (
                  <Badge tone="neutral">when {String(e.condition)}</Badge>
                )}
              </li>
            ))}
          </ul>
        </NodeSection>
      )}
    </div>
  )
}

function NodeSection({
  title,
  type,
  mark,
  children,
}: {
  title: string
  type?: string
  mark: Mark
  children: React.ReactNode
}) {
  const label = { added: 'Added', changed: 'Changed', removed: 'Removed' }[mark]
  return (
    <section className={cx('bk-cr-node', `is-${mark}`)}>
      <header>
        <Badge tone={mark === 'added' ? 'success' : mark === 'removed' ? 'danger' : 'warning'}>
          {label}
        </Badge>
        <strong>{title}</strong>
        {type && <small className="bk-mono">{type}</small>}
      </header>
      <div className="bk-cr-node-body">{children}</div>
    </section>
  )
}

function FieldDiff({
  change,
  nodeType,
  node,
}: {
  change: FieldChange
  nodeType: string
  node?: PipelineNode
}) {
  const label = fieldLabel(change.field)
  if (fieldKind(change) === 'text') {
    return (
      <div className="bk-cr-field">
        <div className="bk-cr-field-label">
          {label} <small>{codeLanguage(nodeType, change.field, node)}</small>
        </div>
        <CodeDiff before={showValue(change.before)} after={showValue(change.after)} />
      </div>
    )
  }
  const before = showValue(change.before)
  const after = showValue(change.after)
  return (
    <div className="bk-cr-field">
      <div className="bk-cr-field-label">{label}</div>
      <div className="bk-cr-values">
        <div className="is-before">
          <span>Before</span>
          <pre>{before || <em>not set</em>}</pre>
        </div>
        <div className="is-after">
          <span>After</span>
          <pre>{after || <em>not set</em>}</pre>
        </div>
      </div>
    </div>
  )
}

function CodeDiff({ before, after }: { before: string; after: string }) {
  const [all, setAll] = useState(false)
  const lines = useMemo(() => lineDiff(before, after), [before, after])
  const cut = useMemo(() => hunks(lines, 3), [lines])
  const shown = all ? [{ lines, hiddenBefore: 0 }] : cut.hunks
  const hidden = cut.hunks.reduce((n, h) => n + h.hiddenBefore, 0) + cut.hiddenAfter
  return (
    <div className="bk-cr-code">
      <table>
        <tbody>
          {shown.map((h, hi) => (
            <HunkRows key={hi} hunk={h} onExpand={() => setAll(true)} />
          ))}
          {!all && cut.hiddenAfter > 0 && (
            <Gap count={cut.hiddenAfter} onExpand={() => setAll(true)} />
          )}
        </tbody>
      </table>
      {!all && hidden > 0 && (
        <button type="button" className="bk-cr-code-all" onClick={() => setAll(true)}>
          Show the whole file
        </button>
      )}
    </div>
  )
}

function HunkRows({
  hunk,
  onExpand,
}: {
  hunk: { lines: ReturnType<typeof lineDiff>; hiddenBefore: number }
  onExpand: () => void
}) {
  return (
    <>
      {hunk.hiddenBefore > 0 && <Gap count={hunk.hiddenBefore} onExpand={onExpand} />}
      {hunk.lines.map((l, i) => (
        <tr key={i} className={`is-${l.kind}`}>
          <td className="bk-cr-ln">{l.before ?? ''}</td>
          <td className="bk-cr-ln">{l.after ?? ''}</td>
          <td className="bk-cr-sign" aria-hidden="true">
            {l.kind === 'add' ? '+' : l.kind === 'del' ? '-' : ''}
          </td>
          <td className="bk-cr-text">
            <span className="bk-sr-only">
              {l.kind === 'add' ? 'added: ' : l.kind === 'del' ? 'removed: ' : ''}
            </span>
            {l.text || ' '}
          </td>
        </tr>
      ))}
    </>
  )
}

function Gap({ count, onExpand }: { count: number; onExpand: () => void }) {
  return (
    <tr className="is-gap">
      <td colSpan={4}>
        <button type="button" onClick={onExpand}>
          {count} unchanged line{count === 1 ? '' : 's'}
        </button>
      </td>
    </tr>
  )
}

// ── Graph ──

function GraphTab({ cr, current }: { cr: ChangeRequest; current: Pipeline }) {
  const g = useMemo(() => graphDiff(current, cr.proposed, cr.diff), [current, cr])
  return (
    <div className="bk-cr-graph">
      <div className="bk-cr-legend" aria-label="Legend">
        <span className="is-added">Added</span>
        <span className="is-changed">Changed</span>
        <span className="is-removed">Removed</span>
      </div>
      <div className="bk-cr-graph-canvas">
        <PipelineGraph nodes={g.nodes} edges={g.edges} marks={g.marks} edgeMarks={g.edgeMarks} />
      </div>
    </div>
  )
}

// ── Test ──

function TestTab({ cr, pipelineId }: { cr: ChangeRequest; pipelineId: string }) {
  const session = useSession()
  const [warned, setWarned] = useState(false)
  const marks = useMemo(() => graphDiff(undefined, cr.proposed, cr.diff).marks, [cr])
  const test = useMutation({ mutationFn: () => pipelineApi.testChange(pipelineId, cr.id, 10) })
  const canRun = session.can('pipelines.run')
  const blocked = !canRun
    ? 'Testing a change needs permission to run this pipeline.'
    : !cr.validation.valid
      ? 'The proposed pipeline is not valid, so it cannot be tested.'
      : null
  const rows = test.data ? compareTest(test.data, marks) : []
  return (
    <div className="bk-cr-test">
      <Callout
        tone="info"
        title="Test the change on a few rows"
        action={
          <Button
            variant="primary"
            icon={<FlaskConical size={15} aria-hidden="true" />}
            loading={test.isPending}
            disabled={Boolean(blocked)}
            title={blocked ?? 'Preview the current and proposed pipelines'}
            onClick={() => (warned ? test.mutate() : setWarned(true))}
          >
            {test.data ? 'Test again' : 'Run test'}
          </Button>
        }
      >
        Runs the current pipeline and the proposed one side by side and keeps the first 10 rows of
        each step. Nothing is written: sinks, notifications and other steps that act outside Brokoli
        are held back, and no run is recorded. Sources are read in full, so a large source takes as
        long as it takes to read.
      </Callout>
      {warned && !test.isPending && !test.data && (
        <ConfirmDialog
          title="Run the proposed code?"
          confirmLabel="Run test"
          onCancel={() => setWarned(false)}
          onConfirm={() => {
            test.mutate()
          }}
        >
          The test runs this change&apos;s code steps as they are proposed, with the pipeline&apos;s
          connections and secrets, in the same sandbox as a run.{' '}
          {cr.created_by_kind === 'agent' &&
            'An agent wrote this change: read its code in the Changes tab first.'}
        </ConfirmDialog>
      )}
      {test.isError && (
        <Callout tone="danger" title="The test could not run">
          {errorMessage(test.error)}
        </Callout>
      )}
      {test.isPending && <Skeleton height={200} />}
      {test.data && (
        <>
          <TopErrors label="current" pv={test.data.current} />
          <TopErrors label="proposed" pv={test.data.proposed} />
          {rows.length === 0 ? (
            <EmptyState title="Nothing to compare">The pipeline has no steps.</EmptyState>
          ) : (
            <div className="bk-cr-compare">
              {rows.map((r) => (
                <Comparison key={r.id} row={r} />
              ))}
            </div>
          )}
        </>
      )}
    </div>
  )
}

function TopErrors({ label, pv }: { label: string; pv: PipelinePreview }) {
  if (!pv.error) return null
  return (
    <Callout
      tone={label === 'proposed' ? 'danger' : 'warning'}
      title={`The ${label} pipeline stopped`}
    >
      {pv.error}
    </Callout>
  )
}

function Comparison({ row }: { row: NodeComparison }) {
  const [open, setOpen] = useState(Boolean(row.verdict) && row.after?.status !== 'held')
  const held = row.after?.status === 'held' || row.after?.status === 'not_run'
  return (
    <section
      className={cx('bk-cr-cmp', row.mark && `is-${row.mark}`, row.verdict && 'has-verdict')}
    >
      <button
        type="button"
        className="bk-cr-cmp-head"
        onClick={() => setOpen((v) => !v)}
        aria-expanded={open}
      >
        <strong>{row.name}</strong>
        <small className="bk-mono">{row.type}</small>
        {row.mark && (
          <Badge
            tone={row.mark === 'added' ? 'success' : row.mark === 'removed' ? 'danger' : 'warning'}
          >
            {row.mark}
          </Badge>
        )}
        <span className="bk-cr-cmp-status">
          <Status s={row.before} /> <span aria-hidden="true">→</span> <Status s={row.after} />
        </span>
        <span className="bk-cr-cmp-verdict">
          {row.verdict ||
            (held
              ? row.after?.reason
              : row.failsInBoth
                ? 'Fails in both versions'
                : 'No difference')}
        </span>
      </button>
      {open && (
        <div className="bk-cr-cmp-body">
          <Sample
            title="Current"
            node={row.before}
            highlight={row.columnsDropped}
            cells={row.changedCells}
          />
          <Sample
            title="Proposed"
            node={row.after}
            highlight={row.columnsAdded}
            cells={row.changedCells}
          />
        </div>
      )}
    </section>
  )
}

function Status({ s }: { s?: PipelinePreview['nodes'][number] }) {
  if (!s) return <Badge tone="neutral">absent</Badge>
  if (s.status === 'held') return <Badge tone="neutral">held</Badge>
  if (s.status === 'not_run') return <Badge tone="neutral">not run</Badge>
  return <StatusBadge status={s.status} />
}

function Sample({
  title,
  node,
  highlight,
  cells,
}: {
  title: string
  node?: PipelinePreview['nodes'][number]
  /** Columns only this side has. */
  highlight: string[]
  /** "row:column" of the values that differ between the two sides. */
  cells: Set<string>
}) {
  const cols = node?.columns ?? []
  const rows = node?.rows ?? []
  return (
    <div className="bk-cr-sample">
      <span className="bk-cr-sample-title">{title}</span>
      {!node ? (
        <p className="bk-muted">Not in this version.</p>
      ) : node.error ? (
        <pre className="bk-cr-error">{node.error}</pre>
      ) : node.status === 'held' || node.status === 'not_run' ? (
        <p className="bk-muted">{node.reason}</p>
      ) : !cols.length ? (
        <p className="bk-muted">No rows.</p>
      ) : (
        <div className="bk-cr-table">
          <table>
            <thead>
              <tr>
                {cols.map((c) => (
                  <th key={c} className={highlight.includes(c) ? 'is-highlight' : undefined}>
                    {c}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {rows.map((r, i) => (
                <tr key={i}>
                  {cols.map((c) => (
                    <td
                      key={c}
                      className={cx(
                        highlight.includes(c) && 'is-highlight',
                        cells.has(`${i}:${c}`) && 'is-changed',
                      )}
                    >
                      {cell(r[c])}
                    </td>
                  ))}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}

function cell(v: unknown): string {
  if (v === null || v === undefined) return ''
  const s = typeof v === 'string' ? v : JSON.stringify(v)
  return s.length > 120 ? `${s.slice(0, 117)}...` : s
}
