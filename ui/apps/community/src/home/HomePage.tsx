import { Link } from 'react-router-dom'
import { Bot, CircleCheck, User } from 'lucide-react'
import type { PipelineSummary, WorkspaceChange } from '@brokoli/api'
import { paths, useNow } from '@brokoli/pipelines'
import { Badge, Callout, Dot, Page, PageHeader, Skeleton, formatRelative } from '@brokoli/ui'
import { useNeedsYou } from './needsYou'
import './home.css'

/*
 * Home: what needs you. The pipelines whose latest run failed, and the
 * changes waiting for a decision (often proposed by an agent). Everything
 * else is on Pipelines and the Dashboard.
 */
export function HomePage() {
  const needs = useNeedsYou()
  const now = useNow(60_000)
  return (
    <Page wide>
      <PageHeader
        eyebrow="Home"
        title="What needs you"
        description="Failed runs, and changes waiting for a decision."
      />
      {needs.errors.length > 0 && (
        <Callout tone="warning" title="Some of this could not be loaded">
          The {needs.errors.join(' and ')} could not be read; what is shown may be incomplete.
        </Callout>
      )}
      {!needs.ready ? (
        <Skeleton height={220} />
      ) : needs.total === 0 ? (
        <section className="cm-home-card cm-home-clear">
          <CircleCheck size={22} aria-hidden="true" />
          <div>
            <strong>Nothing needs you</strong>
            <p>
              No pipeline's latest run failed and no change is waiting for a decision. Everything
              else is on <Link to="/pipelines">Pipelines</Link> and the{' '}
              <Link to="/dashboard">Dashboard</Link>.
            </p>
          </div>
        </section>
      ) : (
        <div className="cm-home-stack">
          <FailedRuns failed={needs.failed} now={now} />
          <ToReview changes={needs.changes} now={now} />
        </div>
      )}
    </Page>
  )
}

function FailedRuns({ failed, now }: { failed: PipelineSummary[]; now: number }) {
  return (
    <section className="cm-home-card" aria-labelledby="cm-home-failed">
      <div className="cm-home-head">
        <Dot tone="danger" />
        <h2 id="cm-home-failed">Failed runs</h2>
        <span>pipelines whose latest run failed</span>
      </div>
      {!failed.length ? (
        <p className="cm-home-none">No pipeline's latest run failed.</p>
      ) : (
        <ul className="cm-home-rows">
          {failed.map((p) => (
            <li key={p.id}>
              <Link className="cm-home-row" to={paths.runs(p.id)}>
                <span className="cm-home-main">
                  <strong className="bk-mono">{p.name}</strong>
                  {p.last_run_error && <span className="cm-home-detail">{p.last_run_error}</span>}
                </span>
                {p.last_run_at && (
                  <span className="cm-home-when">{formatRelative(p.last_run_at, now)}</span>
                )}
                <span className="cm-home-go">Open run</span>
              </Link>
            </li>
          ))}
        </ul>
      )}
    </section>
  )
}

function changeBy(c: WorkspaceChange): string {
  if (c.created_by_kind !== 'agent') return c.created_by_name || 'someone'
  return c.on_behalf_of ? `${c.created_by_name}, for ${c.on_behalf_of}` : c.created_by_name
}

function ToReview({ changes, now }: { changes: WorkspaceChange[]; now: number }) {
  return (
    <section className="cm-home-card" aria-labelledby="cm-home-review">
      <div className="cm-home-head">
        <Dot tone="warning" />
        <h2 id="cm-home-review">Changes to review</h2>
        <span>proposed by people and agents; nothing changes until someone applies them</span>
      </div>
      {!changes.length ? (
        <p className="cm-home-none">No change is waiting for a decision.</p>
      ) : (
        <ul className="cm-home-rows">
          {changes.map((c) => {
            const Icon = c.created_by_kind === 'agent' ? Bot : User
            return (
              <li key={c.id}>
                <Link className="cm-home-row" to={paths.change(c.pipeline_id, c.id)}>
                  <span
                    className={
                      c.created_by_kind === 'agent' ? 'cm-home-who is-agent' : 'cm-home-who'
                    }
                    aria-hidden="true"
                  >
                    <Icon size={15} />
                  </span>
                  <span className="cm-home-main">
                    <strong>{c.title}</strong>
                    <span className="cm-home-detail">
                      <span className="bk-mono">{c.pipeline_name}</span> &middot; {changeBy(c)}{' '}
                      &middot; {formatRelative(c.created_at, now)}
                    </span>
                  </span>
                  {c.stale ? (
                    <Badge tone="warning">Out of date</Badge>
                  ) : !c.validation.valid ? (
                    <Badge tone="danger">Not valid</Badge>
                  ) : (
                    <Badge tone="success">Valid</Badge>
                  )}
                  <span className="bk-button bk-button-primary bk-button-sm">Review</span>
                </Link>
              </li>
            )
          })}
        </ul>
      )}
    </section>
  )
}
