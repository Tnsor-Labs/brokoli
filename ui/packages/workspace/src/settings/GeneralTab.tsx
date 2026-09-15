import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { systemApi } from '@brokoli/api'
import { useSession } from '@brokoli/auth'
import { Button, Callout, ConfirmDialog, Field, Input, Spinner, errorMessage, useToast } from '@brokoli/ui'

export function GeneralTab() {
  const session = useSession()
  const toast = useToast()
  const info = useQuery({ queryKey: ['system', 'info'], queryFn: systemApi.info })
  const [days, setDays] = useState('90')
  const [confirm, setConfirm] = useState(false)
  const n = Number(days)
  const validDays = /^\d+$/.test(days) && n >= 1 && n <= 3650
  const orgScoped = Boolean(session.user?.org_id && session.user.org_id !== 'default')

  return (
    <div className="ws-tab">
      <section className="ws-card">
        <h3>Server</h3>
        {info.isPending ? (
          <p className="ws-inline">
            <Spinner size="sm" /> Loading
          </p>
        ) : info.isError ? (
          <Callout tone="danger">{errorMessage(info.error)}</Callout>
        ) : (
          <dl className="ws-facts">
            <dt>Version</dt>
            <dd className="bk-mono">{info.data.version || 'unknown'}</dd>
            <dt>Runs in progress</dt>
            <dd>{info.data.active_runs}</dd>
            <dt>Concurrent run limit</dt>
            <dd>{info.data.max_concurrent_runs || 'No limit'}</dd>
          </dl>
        )}
      </section>

      {session.can('settings.edit') && (
        <section className="ws-card">
          <h3>Purge old runs</h3>
          <p className="ws-muted">
            Deletes run history, logs and stored outputs for runs that started before the cutoff. Pipelines themselves are not touched.
          </p>
          <div className="ws-inline-form">
            <Field label="Older than (days)" error={days && !validDays ? 'Between 1 and 3650' : undefined}>
              <Input inputMode="numeric" value={days} onChange={(e) => setDays(e.target.value.trim())} />
            </Field>
            <Button variant="danger" disabled={!validDays} onClick={() => setConfirm(true)}>
              Purge runs
            </Button>
          </div>
        </section>
      )}

      {confirm && (
        <ConfirmDialog
          title={`Delete every run older than ${n} days?`}
          tone="danger"
          confirmLabel="Purge runs"
          confirmText="purge"
          onCancel={() => setConfirm(false)}
          onConfirm={async () => {
            const result = await systemApi.purge(n)
            toast.success(`Deleted ${result.deleted} run${result.deleted === 1 ? '' : 's'}`, `Older than ${result.days} days.`)
            setConfirm(false)
            void info.refetch()
          }}
        >
          <p>
            {orgScoped
              ? 'This removes those runs for your whole organisation, in every workspace.'
              : 'This removes those runs for every pipeline on this server, in every workspace.'}{' '}
            It cannot be undone.
          </p>
        </ConfirmDialog>
      )}
    </div>
  )
}
