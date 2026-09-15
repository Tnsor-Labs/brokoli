import { useMemo, useState } from 'react'
import { Copy, KeyRound, X } from 'lucide-react'
import type { Pipeline } from '@brokoli/api'
import { Badge, Button, Drawer, Field, Input, Select, Textarea, useToast } from '@brokoli/ui'
import { sanitizeTags } from '../document'
import { MapEditor } from '../forms/fields'
import { DependencyPicker } from './DependencyPicker'

const NAME_FORBIDDEN = /[<>"'&]/

function zones(current?: string) {
  let list: string[] = []
  try {
    list = (Intl as unknown as { supportedValuesOf?: (k: string) => string[] }).supportedValuesOf?.('timeZone') ?? []
  } catch {
    list = []
  }
  return [...new Set(['UTC', ...list, ...(current ? [current] : [])])]
}

function generateToken() {
  const bytes = crypto.getRandomValues(new Uint8Array(24))
  return `whk_${Array.from(bytes, (b) => b.toString(16).padStart(2, '0')).join('')}`
}

/*
 * Pipeline-level settings. Every change is part of the document and is
 * written by the next save; fields this drawer does not show (hooks,
 * typed parameters, extensions) are left exactly as they are.
 */
export function SettingsDrawer({
  pipeline,
  readonly,
  onChange,
  onClose,
}: {
  pipeline: Pipeline
  readonly: boolean
  onChange: (patch: Partial<Pipeline>) => void
  onClose: () => void
}) {
  const toast = useToast()
  const [tag, setTag] = useState('')
  const tags = pipeline.tags ?? []
  const tzs = useMemo(() => zones(pipeline.sla_timezone), [pipeline.sla_timezone])
  const nameError = !pipeline.name.trim()
    ? 'A pipeline needs a name'
    : NAME_FORBIDDEN.test(pipeline.name)
      ? 'Names cannot contain < > " \' or &'
      : pipeline.name.length > 255
        ? 'At most 255 characters'
        : undefined
  const webhookUrl = `${window.location.origin}/api/pipelines/${encodeURIComponent(pipeline.id)}/webhook`
  const addTags = (raw: string) => {
    const next = sanitizeTags([...tags, ...raw.split(',')])
    if (next.length !== tags.length) onChange({ tags: next })
    setTag('')
  }
  const copy = async (text: string, what: string) => {
    try {
      await navigator.clipboard.writeText(text)
      toast.success(`${what} copied`)
    } catch (e) {
      toast.error(`Could not copy the ${what.toLowerCase()}`, e)
    }
  }

  return (
    <Drawer title="Pipeline settings" description="Changes are saved with the pipeline." width={560} onClose={onClose}>
      <fieldset className="ps-settings" disabled={readonly}>
        <section>
          <h4>General</h4>
          <Field label="Name" error={nameError} required>
            <Input value={pipeline.name} maxLength={255} onChange={(e) => onChange({ name: e.target.value })} />
          </Field>
          <Field label="Description" hint={`${(pipeline.description ?? '').length} / 2000`}>
            <Textarea rows={3} maxLength={2000} value={pipeline.description ?? ''} onChange={(e) => onChange({ description: e.target.value })} />
          </Field>
          <div className="bk-field">
            <div className="bk-field-label">
              <label htmlFor="ps-tag-input">Tags</label>
            </div>
            <div className="ps-tags">
              {tags.map((t) => (
                <span key={t} className="ps-tag">
                  {t}
                  {!readonly && (
                    <button type="button" aria-label={`Remove tag ${t}`} onClick={() => onChange({ tags: tags.filter((x) => x !== t) })}>
                      <X size={12} aria-hidden="true" />
                    </button>
                  )}
                </span>
              ))}
              <input
                id="ps-tag-input"
                className="ps-tag-input"
                value={tag}
                placeholder={tags.length ? 'Add another' : 'Add a tag and press Enter'}
                onChange={(e) => (e.target.value.includes(',') ? addTags(e.target.value) : setTag(e.target.value))}
                onKeyDown={(e) => {
                  if (e.key === 'Enter' && tag.trim()) {
                    e.preventDefault()
                    addTags(tag)
                  }
                }}
                onBlur={() => tag.trim() && addTags(tag)}
              />
            </div>
          </div>
        </section>

        <section>
          <h4>Default run parameters</h4>
          <p className="ps-form-note">Used by every run unless a run supplies its own value. Reference them as {'${param.name}'} in node settings.</p>
          <MapEditor value={pipeline.params} onChange={(v) => onChange({ params: v ?? {} })} keyPlaceholder="name" valuePlaceholder="default value" addLabel="Add a parameter" />
        </section>

        <section>
          <h4>
            Service level <Badge tone="accent">Enterprise</Badge>
          </h4>
          <p className="ps-form-note">Stored in every edition. Breach monitoring and alerts need an Enterprise deployment.</p>
          <div className="ps-form-row">
            <Field label="Finish by">
              <Input type="time" value={pipeline.sla_deadline ?? ''} onChange={(e) => onChange({ sla_deadline: e.target.value || undefined })} />
            </Field>
            <Field label="Time zone">
              <Select value={pipeline.sla_timezone ?? ''} onChange={(e) => onChange({ sla_timezone: e.target.value || undefined })}>
                <option value="">UTC (default)</option>
                {tzs.map((z) => (
                  <option key={z} value={z}>
                    {z}
                  </option>
                ))}
              </Select>
            </Field>
          </div>
        </section>

        <section>
          <h4>Webhook trigger</h4>
          <p className="ps-form-note">Lets another system start a run with a POST request. The token takes effect when you save.</p>
          {pipeline.webhook_token ? (
            <>
              <div className="ps-webhook">
                <code>{`${pipeline.webhook_token.slice(0, 12)}${'•'.repeat(12)}`}</code>
                <Button size="sm" icon={<Copy size={13} aria-hidden="true" />} onClick={() => void copy(pipeline.webhook_token!, 'Token')}>
                  Copy token
                </Button>
                {!readonly && (
                  <Button size="sm" variant="danger" onClick={() => onChange({ webhook_token: undefined })}>
                    Revoke
                  </Button>
                )}
              </div>
              <div className="ps-webhook-url">
                <code>POST {webhookUrl}</code>
                <Button size="sm" variant="ghost" icon={<Copy size={13} aria-hidden="true" />} onClick={() => void copy(webhookUrl, 'URL')}>
                  Copy URL
                </Button>
              </div>
              <p className="ps-form-note">Send the token in the X-Webhook-Token header. At most one call every 10 seconds; drafts refuse webhook runs.</p>
            </>
          ) : (
            <Button size="sm" icon={<KeyRound size={14} aria-hidden="true" />} onClick={() => onChange({ webhook_token: generateToken() })} disabled={readonly}>
              Generate a token
            </Button>
          )}
        </section>

        <section>
          <h4>Upstream dependencies</h4>
          <DependencyPicker
            pipelineId={pipeline.id}
            rules={pipeline.dependency_rules ?? []}
            legacy={pipeline.depends_on ?? []}
            readonly={readonly}
            onChange={(rules, legacy) => onChange({ dependency_rules: rules.length ? rules : undefined, depends_on: legacy.length ? legacy : undefined })}
          />
        </section>
      </fieldset>
    </Drawer>
  )
}
