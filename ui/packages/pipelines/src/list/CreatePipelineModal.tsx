import { useState, type FormEvent } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useNavigate } from 'react-router-dom'
import { Check, Code2, FilePlus2, FileText, GitMerge, Globe, LayoutTemplate } from 'lucide-react'
import { pipelineApi, templateApi } from '@brokoli/api'
import { Button, Callout, Field, Input, Modal, Skeleton, Textarea, cx, errorMessage, useToast } from '@brokoli/ui'
import { keys, paths } from '../keys'

const SCRATCH = '__scratch__'

/** Template icon slugs seeded by core (pkg/templates): file, api, merge, code. */
function templateIcon(slug: string) {
  const props = { size: 18, 'aria-hidden': true } as const
  if (slug === 'file') return <FileText {...props} />
  if (slug === 'api') return <Globe {...props} />
  if (slug === 'merge') return <GitMerge {...props} />
  if (slug === 'code') return <Code2 {...props} />
  return <LayoutTemplate {...props} />
}

function browserTimezone() {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC'
  } catch {
    return 'UTC'
  }
}

/*
 * New pipeline: start from an empty draft or from a server template.
 * A scratch pipeline is created as a draft (it cannot run until it is
 * published from the editor); a template is created ready to run, which is
 * why the server validates it fully. The server's error text is shown as-is
 * (duplicate names, invalid characters), never replaced by a generic line.
 */
export function CreatePipelineModal({ initialTemplate, onClose }: { initialTemplate?: string; onClose: () => void }) {
  const templates = useQuery({ queryKey: keys.templates, queryFn: templateApi.list, staleTime: 60_000 })
  const [choice, setChoice] = useState(initialTemplate ?? SCRATCH)
  const [name, setName] = useState('')
  const [description, setDescription] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const toast = useToast()
  const template = templates.data?.find((t) => t.id === choice)

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (!name.trim() || busy) return
    setBusy(true)
    setError('')
    try {
      const created = await pipelineApi.create({
        name: name.trim(),
        description: description.trim() || template?.description || '',
        enabled: true,
        nodes: template?.nodes ?? [],
        edges: template?.edges ?? [],
        ...(template ? {} : { draft: true }),
        schedule_timezone: browserTimezone(),
      })
      await queryClient.invalidateQueries({ queryKey: keys.summary })
      toast.success('Pipeline created', created.name)
      onClose()
      navigate(paths.editor(created.id))
    } catch (err) {
      setError(errorMessage(err))
      setBusy(false)
    }
  }

  return (
    <Modal
      title="Create a pipeline"
      description="Start from an empty draft or from one of the workspace templates."
      size="lg"
      onClose={onClose}
      dismissible={!busy}
      footer={
        <>
          <Button variant="ghost" onClick={onClose} disabled={busy}>
            Cancel
          </Button>
          <Button variant="primary" type="submit" form="bk-create-pipeline" loading={busy} disabled={!name.trim()}>
            Create and open editor
          </Button>
        </>
      }
    >
      <form id="bk-create-pipeline" className="bk-create" onSubmit={submit}>
        <fieldset className="bk-create-templates">
          <legend>Starting point</legend>
          <div className="bk-create-grid" role="radiogroup" aria-label="Starting point">
            <button
              type="button"
              role="radio"
              aria-checked={choice === SCRATCH}
              className={cx('bk-template', choice === SCRATCH && 'is-selected')}
              onClick={() => setChoice(SCRATCH)}
            >
              <span className="bk-template-icon">
                <FilePlus2 size={18} aria-hidden="true" />
              </span>
              <span className="bk-template-text">
                <strong>Empty draft</strong>
                <small>Build it node by node. Drafts do not run until you publish them.</small>
              </span>
              {choice === SCRATCH && <Check className="bk-template-check" size={16} aria-hidden="true" />}
            </button>
            {templates.isPending &&
              [0, 1, 2].map((i) => <Skeleton key={i} height={74} className="bk-template-skeleton" />)}
            {templates.data?.map((t) => (
              <button
                key={t.id}
                type="button"
                role="radio"
                aria-checked={choice === t.id}
                className={cx('bk-template', choice === t.id && 'is-selected')}
                onClick={() => setChoice(t.id)}
              >
                <span className="bk-template-icon">{templateIcon(t.icon)}</span>
                <span className="bk-template-text">
                  <strong>{t.name}</strong>
                  <small>
                    {t.description || 'Template'} · {t.nodes?.length ?? 0} nodes
                  </small>
                </span>
                {choice === t.id && <Check className="bk-template-check" size={16} aria-hidden="true" />}
              </button>
            ))}
          </div>
          {templates.isError && (
            <Callout tone="warning">Templates could not be loaded ({errorMessage(templates.error)}). You can still start from an empty draft.</Callout>
          )}
        </fieldset>
        <Field label="Name" required hint="Letters, numbers, spaces, dashes and underscores. Names must be unique.">
          <Input value={name} onChange={(e) => setName(e.target.value)} placeholder="daily-customer-sync" autoFocus maxLength={255} />
        </Field>
        <Field label="Description">
          <Textarea
            rows={3}
            value={description}
            onChange={(e) => setDescription(e.target.value)}
            placeholder={template?.description || 'What does this pipeline do?'}
            maxLength={2000}
          />
        </Field>
        {error && <Callout tone="danger" title="The pipeline was not created">{error}</Callout>}
      </form>
    </Modal>
  )
}
