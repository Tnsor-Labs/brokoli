import type { SecretShape, SecretStore } from '@brokoli/api'
import { Field, Input, Select } from '@brokoli/ui'
import { secretRefProblem, type SecretRefParts } from './reference'

export const EMPTY_REF: SecretRefParts = { store: '', path: '', version: '', field: '' }

/**
 * Picks a secret in one of the workspace's stores: the store, the secret's
 * path as the secret manager spells it, an optional version, and a field
 * when the store's secrets are maps (required) or may be (optional).
 */
export function SecretRefPicker({
  stores,
  shapeOf,
  value,
  onChange,
  idPrefix,
}: {
  stores: SecretStore[]
  shapeOf: (store: string) => SecretShape | undefined
  value: SecretRefParts
  onChange: (next: SecretRefParts) => void
  idPrefix: string
}) {
  const shape = shapeOf(value.store)
  const problem = secretRefProblem(value, shape)
  const set = (patch: Partial<SecretRefParts>) => onChange({ ...value, ...patch })
  return (
    <div className="ws-secret-ref">
      <div className="ws-row">
        <Field label="Secret store" required>
          <Select
            id={`${idPrefix}-store`}
            value={value.store}
            onChange={(e) => set({ store: e.target.value })}
          >
            <option value="">Choose a store</option>
            {stores.map((s) => (
              <option key={s.id} value={s.name}>
                {s.name} ({s.provider})
              </option>
            ))}
          </Select>
        </Field>
        <Field label="Version" hint="Empty for the current version.">
          <Input
            id={`${idPrefix}-version`}
            mono
            value={value.version}
            placeholder="Current"
            onChange={(e) => set({ version: e.target.value.trim() })}
          />
        </Field>
      </div>
      <Field
        label="Secret path"
        required
        hint="As your secret manager names it, for example prod/warehouse or /prod/warehouse/password."
      >
        <Input
          id={`${idPrefix}-path`}
          mono
          value={value.path}
          placeholder="prod/warehouse"
          onChange={(e) => set({ path: e.target.value.trim() })}
        />
      </Field>
      {(shape === 'map' || shape === 'either') && (
        <Field
          label="Field"
          required={shape === 'map'}
          hint={
            shape === 'map'
              ? 'The secret is a map of named fields: name the one to use.'
              : 'Only when the secret is a JSON object of fields.'
          }
        >
          <Input
            id={`${idPrefix}-field`}
            mono
            value={value.field}
            placeholder="password"
            onChange={(e) => set({ field: e.target.value.trim() })}
          />
        </Field>
      )}
      {problem && value.store && <p className="ws-warning">{problem}</p>}
    </div>
  )
}
