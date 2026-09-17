import { useMemo } from 'react'
import { AlertTriangle } from 'lucide-react'
import { previewTemplate } from './template'

/**
 * A live preview of a templated field: what its ${...} variables render to for a
 * sample data interval, plus inline validation of the date filters. Shown only
 * once the value actually contains a variable. Purely client-side — the grammar
 * lives in ./template and matches the engine.
 */
export function TemplatePreview({ value }: { value: string }) {
  const { segments, issues, usesInterval } = useMemo(() => previewTemplate(value), [value])

  if (!value.includes('${')) return null

  return (
    <div className="ps-template-preview">
      <div className="ps-template-line">
        <span className="ps-template-caption">Preview</span>
        <code>
          {segments.map((seg, i) => {
            if (seg.kind === 'text') return <span key={i}>{seg.text}</span>
            if (seg.kind === 'value')
              return (
                <span key={i} className="ps-template-value" title={seg.raw}>
                  {seg.text}
                </span>
              )
            if (seg.kind === 'deferred')
              return (
                <span key={i} className="ps-template-deferred" title={`${seg.ref} is filled in when the run starts`}>
                  {seg.raw}
                </span>
              )
            return (
              <span key={i} className="ps-template-invalid" title={seg.reason}>
                {seg.raw}
              </span>
            )
          })}
        </code>
      </div>
      {issues.length > 0 && (
        <ul className="ps-template-issues">
          {issues.map((issue, i) => (
            <li key={i}>
              <AlertTriangle size={13} aria-hidden="true" />
              <span>
                <code>{issue.raw}</code> stays as written in the output: {issue.reason}
              </span>
            </li>
          ))}
        </ul>
      )}
      <p className="ps-template-note">
        Timestamps use a sample interval (14 Mar 2024). Parameters, variables and secrets are filled in when the run starts.
        {usesInterval && ' Interval references are empty on a manual run, and then the whole reference stays visible.'}
      </p>
    </div>
  )
}
