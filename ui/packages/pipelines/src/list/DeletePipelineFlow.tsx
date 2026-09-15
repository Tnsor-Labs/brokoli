import { useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { ApiError, pipelineApi, type DeleteConflict, type DeleteResolve } from '@brokoli/api'
import { Button, Callout, ConfirmDialog, Input, Modal, cx, errorMessage, useToast } from '@brokoli/ui'
import { keys } from '../keys'

function isConflict(error: unknown): error is ApiError & { body: DeleteConflict } {
  return (
    error instanceof ApiError &&
    error.status === 409 &&
    Array.isArray((error.body as DeleteConflict | null)?.dependents)
  )
}

/*
 * Delete with the server's dependents protocol: a plain delete first; if
 * other pipelines depend on this one the server answers 409 with their
 * names, and the user chooses to decouple them or delete them too. After
 * either path the list is refetched, because a cascade removes the whole
 * transitive set, not only the direct dependents the 409 listed.
 */
export function DeletePipelineFlow({
  pipeline,
  onClose,
  onDeleted,
}: {
  pipeline: { id: string; name: string }
  onClose: () => void
  onDeleted?: () => void
}) {
  const [conflict, setConflict] = useState<DeleteConflict | null>(null)
  const [mode, setMode] = useState<Exclude<DeleteResolve, 'abort'>>('decouple')
  const [typed, setTyped] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const queryClient = useQueryClient()
  const toast = useToast()

  const finish = async (message: string) => {
    await queryClient.invalidateQueries({ queryKey: keys.summary })
    queryClient.removeQueries({ queryKey: keys.pipeline(pipeline.id) })
    toast.success(message)
    onDeleted?.()
    onClose()
  }

  if (!conflict)
    return (
      <ConfirmDialog
        title={`Delete ${pipeline.name}?`}
        tone="danger"
        confirmLabel="Delete pipeline"
        onCancel={onClose}
        onConfirm={async () => {
          try {
            await pipelineApi.remove(pipeline.id)
          } catch (e) {
            if (isConflict(e)) {
              setConflict(e.body)
              return
            }
            throw e
          }
          await finish(`Deleted ${pipeline.name}`)
        }}
      >
        <p>This permanently deletes the pipeline together with its run history, logs and stored previews.</p>
      </ConfirmDialog>
    )

  const resolve = async () => {
    setBusy(true)
    setError('')
    try {
      await pipelineApi.remove(pipeline.id, mode)
      await finish(
        mode === 'cascade' ? `Deleted ${pipeline.name} and the pipelines that depend on it` : `Deleted ${pipeline.name}; dependents were decoupled`,
      )
    } catch (e) {
      setError(errorMessage(e))
      setBusy(false)
    }
  }

  const count = conflict.dependents.length
  return (
    <Modal
      title={`Other pipelines depend on ${pipeline.name}`}
      description={`${count} pipeline${count === 1 ? '' : 's'} list${count === 1 ? 's' : ''} it as an upstream dependency. Choose how to handle ${count === 1 ? 'it' : 'them'}.`}
      size="md"
      onClose={onClose}
      dismissible={!busy}
      footer={
        <>
          <Button variant="ghost" onClick={onClose} disabled={busy}>
            Cancel
          </Button>
          <Button
            variant={mode === 'cascade' ? 'danger' : 'primary'}
            loading={busy}
            disabled={mode === 'cascade' && typed !== pipeline.name}
            onClick={resolve}
          >
            {mode === 'cascade' ? 'Delete all' : 'Decouple and delete'}
          </Button>
        </>
      }
    >
      <div className="bk-delete-flow">
        <ul className="bk-dependents">
          {conflict.dependents.map((d) => (
            <li key={d.id}>{d.name}</li>
          ))}
        </ul>
        <div className="bk-delete-options" role="radiogroup" aria-label="How to handle dependents">
          <button
            type="button"
            role="radio"
            aria-checked={mode === 'decouple'}
            className={cx('bk-delete-option', mode === 'decouple' && 'is-selected')}
            onClick={() => setMode('decouple')}
          >
            <strong>Decouple</strong>
            <span>Remove the dependency from each pipeline listed above, then delete {pipeline.name}. The others keep running on their own schedule.</span>
          </button>
          <button
            type="button"
            role="radio"
            aria-checked={mode === 'cascade'}
            className={cx('bk-delete-option', 'is-danger', mode === 'cascade' && 'is-selected')}
            onClick={() => setMode('cascade')}
          >
            <strong>Delete everything downstream</strong>
            <span>Also delete every pipeline that depends on this one, directly or through another pipeline. This cannot be undone.</span>
          </button>
        </div>
        {mode === 'cascade' && (
          <label className="bk-confirm-type">
            <span>
              Type <code>{pipeline.name}</code> to confirm
            </span>
            <Input mono value={typed} onChange={(e) => setTyped(e.target.value)} autoComplete="off" />
          </label>
        )}
        {error && <Callout tone="danger">{error}</Callout>}
      </div>
    </Modal>
  )
}
