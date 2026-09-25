import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { X } from 'lucide-react'
import { pipelineApi, type Pipeline, type PipelineVersion } from '@brokoli/api'
import { Button, Callout, ConfirmDialog, IconButton, Spinner, errorMessage, formatDateTime, formatRelative, useToast } from '@brokoli/ui'

/*
 * Version history. Every save writes a version; restoring writes a new one
 * ("rollback to vN") rather than rewriting history. The server fully
 * validates the snapshot being restored, so an incomplete draft version can
 * be refused; its message is shown, not replaced.
 */
export function VersionsPanel({
  pipelineId,
  dirty,
  readonly,
  onClose,
  onRestored,
}: {
  pipelineId: string
  dirty: boolean
  readonly: boolean
  onClose: () => void
  onRestored: (pipeline: Pipeline) => void
}) {
  const toast = useToast()
  const queryClient = useQueryClient()
  const versions = useQuery({ queryKey: ['pipeline', pipelineId, 'versions'], queryFn: () => pipelineApi.versions(pipelineId) })
  const [target, setTarget] = useState<PipelineVersion | null>(null)

  return (
    <div className="ps-versions">
      <header className="ps-panel-head">
        <div>
          <h3>Version history</h3>
          <p>Newest first. The server keeps the latest 50.</p>
        </div>
        <IconButton label="Close version history" onClick={onClose}>
          <X size={16} aria-hidden="true" />
        </IconButton>
      </header>
      <div className="ps-versions-body">
        {versions.isPending ? (
          <div className="bk-inline-loading">
            <Spinner size="sm" /> Loading versions
          </div>
        ) : versions.isError ? (
          <Callout tone="danger">{errorMessage(versions.error)}</Callout>
        ) : !versions.data.length ? (
          <p className="ps-form-note">No versions yet. Saving creates the first one.</p>
        ) : (
          <ol className="ps-version-list">
            {versions.data.map((v, i) => (
              <li key={v.version} className={i === 0 ? 'is-current' : undefined}>
                <div>
                  <strong>
                    v{v.version} {i === 0 && <span className="ps-version-current">current</span>}
                  </strong>
                  <span title={formatDateTime(v.created_at)}>{formatRelative(v.created_at)}</span>
                  <small>{v.message || 'Saved'}</small>
                </div>
                {i > 0 && !readonly && (
                  <Button size="sm" variant="ghost" onClick={() => setTarget(v)}>
                    Restore
                  </Button>
                )}
              </li>
            ))}
          </ol>
        )}
      </div>
      {target && (
        <ConfirmDialog
          title={`Restore version ${target.version}?`}
          confirmLabel="Restore"
          tone={dirty ? 'danger' : 'primary'}
          onCancel={() => setTarget(null)}
          onConfirm={async () => {
            const restored = await pipelineApi.rollback(pipelineId, target.version)
            void queryClient.invalidateQueries({ queryKey: ['pipeline', pipelineId, 'versions'] })
            toast.success(`Restored version ${target.version}`, 'A new version was recorded for the restore.')
            setTarget(null)
            onRestored(restored)
          }}
        >
          <p>
            The pipeline goes back to how it was on {formatDateTime(target.created_at)}, saved as a new version.
            {dirty && ' Your unsaved changes in the editor are discarded.'}
          </p>
        </ConfirmDialog>
      )}
    </div>
  )
}
