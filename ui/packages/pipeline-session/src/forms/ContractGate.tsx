import { useState, type ChangeEvent } from 'react'
import { Button, Callout, Field, Input, Select, Textarea } from '@brokoli/ui'
import { Section, str, type FormCtx, type TypeFormProps } from './fields'

type Predicate = {
  op: string
  type?: string
  format?: string
  pattern?: string
  min?: number
  max?: number
  values?: unknown[]
}

type ContractRule = {
  id: string
  version?: string
  kind: 'record' | 'stream'
  path: string
  predicate: Predicate
  on_breach: { severity?: string; action: string }
}

export type ContractDocument = {
  ir_version: string
  contract: { id: string; version: string }
  input: { kind: string }
  rules: ContractRule[]
}

const DEFAULT_CONTRACT: ContractDocument = {
  ir_version: '1.0',
  contract: { id: '', version: '1' },
  input: { kind: 'record-stream' },
  rules: [],
}

// Every predicate mandates exactly one rule kind -- the contract
// validator refuses the other ("required must be a record rule",
// "unique must be a stream rule"). There is no combination where the
// user has a real choice, so the kind is declared here beside the
// operator it belongs to and derived, never picked.
const OPERATORS = [
  { value: 'required', label: 'Required', kind: 'record' },
  { value: 'not_null', label: 'Not null', kind: 'record' },
  { value: 'type', label: 'Type', kind: 'record' },
  { value: 'format', label: 'Format', kind: 'record' },
  { value: 'regex', label: 'Regular expression', kind: 'record' },
  { value: 'range', label: 'Numeric range', kind: 'record' },
  { value: 'enum', label: 'Allowed values', kind: 'record' },
  { value: 'unique', label: 'Unique in stream', kind: 'stream' },
  { value: 'count', label: 'Stream row count', kind: 'stream' },
] as const satisfies ReadonlyArray<{ value: string; label: string; kind: ContractRule['kind'] }>

// kindForPredicate answers what the contract validator will demand.
// Unknown operators keep a record rule, which is what an imported
// contract using a predicate this UI does not list should stay as
// rather than being silently rewritten.
export function kindForPredicate(op: string): ContractRule['kind'] {
  return OPERATORS.find((operator) => operator.value === op)?.kind ?? 'record'
}

const ACTIONS = [
  { value: 'warn', label: 'Warn and continue' },
  { value: 'quarantine', label: 'Quarantine the record' },
  { value: 'reject', label: 'Reject the run' },
  { value: 'halt', label: 'Halt immediately' },
]

function isObject(value: unknown): value is Record<string, any> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

/** Parse the optional import format without mutating node config on failure. */
export function parseImportedContract(text: string): ContractDocument {
  const parsed: unknown = JSON.parse(text)
  if (!isObject(parsed)) throw new Error('Contract JSON must be an object, not an array or scalar.')
  const candidate =
    typeof parsed.ir_version === 'string'
      ? parsed
      : isObject(parsed.contract) && typeof parsed.contract.ir_version === 'string'
        ? parsed.contract
        : undefined
  if (!candidate)
    throw new Error('Expected a canonical contract object with ir_version, or {"contract": {...}}.')
  if (
    !isObject(candidate.contract) ||
    typeof candidate.contract.id !== 'string' ||
    typeof candidate.contract.version !== 'string'
  ) {
    throw new Error('Contract metadata must include contract.id and contract.version.')
  }
  if (!isObject(candidate.input) || candidate.input.kind !== 'record-stream') {
    throw new Error('Contract input.kind must be "record-stream".')
  }
  if (!Array.isArray(candidate.rules)) throw new Error('Contract rules must be an array.')
  return candidate as ContractDocument
}

export function applyPredicateSelection(rule: ContractRule, op: string): ContractRule {
  const predicate = { ...rule.predicate, op }
  return {
    ...rule,
    // Both directions. Deriving only towards stream left the reverse
    // open: choosing Stream and then a record predicate kept kind
    // "stream", which the validator refuses.
    kind: kindForPredicate(op),
    // count is the one predicate that also fixes the path.
    path: op === 'count' ? '$' : rule.path,
    predicate,
  }
}

function normalizeContract(value: unknown): ContractDocument {
  if (!isObject(value)) return DEFAULT_CONTRACT
  return {
    ...DEFAULT_CONTRACT,
    ...value,
    contract: { ...DEFAULT_CONTRACT.contract, ...(isObject(value.contract) ? value.contract : {}) },
    input: { ...DEFAULT_CONTRACT.input, ...(isObject(value.input) ? value.input : {}) },
    rules: Array.isArray(value.rules) ? value.rules : [],
  } as ContractDocument
}

function RuleEditor({
  ctx,
  rule,
  index,
  onChange,
  onRemove,
}: {
  ctx: FormCtx
  rule: ContractRule
  index: number
  onChange: (next: ContractRule) => void
  onRemove: () => void
}) {
  const predicate = rule.predicate ?? { op: 'required' }
  const setRule = (patch: Partial<ContractRule>) => onChange({ ...rule, ...patch })
  const setPredicate = (patch: Partial<Predicate>) => {
    const next = patch.op
      ? applyPredicateSelection(rule, patch.op)
      : { ...rule, predicate: { ...predicate, ...patch } }
    setRule(next)
  }
  const enumText = JSON.stringify(predicate.values ?? [], null, 2)
  const updateNumber = (name: 'min' | 'max', value: string) => {
    if (value === '') {
      const next = { ...predicate }
      delete next[name]
      setRule({ predicate: next })
    } else if (Number.isFinite(Number(value))) {
      setPredicate({ [name]: Number(value) })
    }
  }

  return (
    <div className="ps-rule">
      <div className="ps-rule-head">
        <span className="ps-rule-index">{index + 1}</span>
        <strong>{rule.id || 'New contract rule'}</strong>
        <Button size="sm" variant="danger" onClick={onRemove} disabled={ctx.readonly}>
          Remove
        </Button>
      </div>
      <div className="ps-rule-body">
        <Field label="Rule ID" required error={!rule.id ? 'Required' : undefined}>
          <Input
            value={rule.id}
            mono
            disabled={ctx.readonly}
            onChange={(event) => setRule({ id: event.target.value })}
          />
        </Field>
        <Field
          label="Rule kind"
          hint="Set by the predicate: record rules run per row, stream rules inspect the whole input."
        >
          <Input value={rule.kind} mono disabled />
        </Field>
        <Field
          label="JSON path"
          hint="Use paths such as $.email, or $ for a stream count."
          required
        >
          <Input
            value={rule.path}
            mono
            disabled={ctx.readonly || predicate.op === 'count'}
            onChange={(event) => setRule({ path: event.target.value })}
          />
        </Field>
        <Field label="Predicate">
          <Select
            value={predicate.op}
            disabled={ctx.readonly}
            onChange={(event) => setPredicate({ op: event.target.value })}
          >
            {OPERATORS.map((operator) => (
              <option key={operator.value} value={operator.value}>
                {operator.label}
              </option>
            ))}
          </Select>
        </Field>
        {predicate.op === 'type' && (
          <Field label="Expected type">
            <Select
              value={predicate.type ?? ''}
              disabled={ctx.readonly}
              onChange={(event) => setPredicate({ type: event.target.value })}
            >
              <option value="">Choose a type</option>
              {['string', 'number', 'integer', 'boolean', 'object', 'array'].map((type) => (
                <option key={type} value={type}>
                  {type}
                </option>
              ))}
            </Select>
          </Field>
        )}
        {predicate.op === 'format' && (
          <Field label="Format">
            <Select
              value={predicate.format ?? 'email'}
              disabled={ctx.readonly}
              onChange={(event) => setPredicate({ format: event.target.value })}
            >
              <option value="email">Email</option>
            </Select>
          </Field>
        )}
        {predicate.op === 'regex' && (
          <Field label="Pattern" hint="Uses Go's RE2 regular-expression syntax." required>
            <Input
              value={predicate.pattern ?? ''}
              mono
              disabled={ctx.readonly}
              onChange={(event) => setPredicate({ pattern: event.target.value })}
            />
          </Field>
        )}
        {predicate.op === 'range' || predicate.op === 'count' ? (
          <>
            <Field label="Minimum">
              <Input
                type="number"
                value={predicate.min ?? ''}
                disabled={ctx.readonly}
                onChange={(event) => updateNumber('min', event.target.value)}
              />
            </Field>
            <Field label="Maximum">
              <Input
                type="number"
                value={predicate.max ?? ''}
                disabled={ctx.readonly}
                onChange={(event) => updateNumber('max', event.target.value)}
              />
            </Field>
          </>
        ) : null}
        {predicate.op === 'enum' && (
          <Field
            label="Allowed values"
            hint={'Enter a JSON array, for example ["paid", "refunded"].'}
            required
          >
            <Textarea
              value={enumText}
              mono
              rows={4}
              disabled={ctx.readonly}
              onChange={(event) => {
                try {
                  const values = JSON.parse(event.target.value)
                  if (Array.isArray(values)) setPredicate({ values })
                } catch {
                  // Keep invalid draft text local to the control until it parses.
                }
              }}
            />
          </Field>
        )}
        <Field label="When it fails">
          <Select
            value={rule.on_breach?.action ?? ''}
            disabled={ctx.readonly}
            onChange={(event) =>
              setRule({ on_breach: { ...rule.on_breach, action: event.target.value } })
            }
          >
            <option value="">Choose an action</option>
            {ACTIONS.map((action) => (
              <option key={action.value} value={action.value}>
                {action.label}
              </option>
            ))}
          </Select>
        </Field>
        <Field label="Severity">
          <Select
            value={rule.on_breach?.severity ?? 'error'}
            disabled={ctx.readonly}
            onChange={(event) =>
              setRule({ on_breach: { ...rule.on_breach, severity: event.target.value } })
            }
          >
            <option value="error">Error</option>
            <option value="warning">Warning</option>
          </Select>
        </Field>
      </div>
    </div>
  )
}

export function ContractGate({ ctx }: TypeFormProps) {
  const document = normalizeContract(ctx.get('contract'))
  const [importText, setImportText] = useState('')
  const [importError, setImportError] = useState('')
  const setContract = (next: ContractDocument, historyKey = `contract:${ctx.node.id}`) =>
    ctx.set({ contract: next }, historyKey)
  const updateRule = (index: number, rule: ContractRule) =>
    setContract({
      ...document,
      rules: document.rules.map((current, i) => (i === index ? rule : current)),
    })
  const importContract = (text: string) => {
    setImportText(text)
    try {
      setContract(parseImportedContract(text))
      setImportError('')
    } catch (error) {
      setImportError(error instanceof Error ? error.message : 'Invalid contract JSON.')
    }
  }
  const readFile = (event: ChangeEvent<HTMLInputElement>) => {
    const file = event.target.files?.[0]
    if (file)
      void file
        .text()
        .then(importContract)
        .catch(() => setImportError('Could not read that file.'))
  }

  return (
    <>
      <Section
        title="Contract"
        description="Build a versioned data contract below. The server validates the canonical contract before processing rows."
      >
        <Field label="Contract ID" required error={!document.contract.id ? 'Required' : undefined}>
          <Input
            value={document.contract.id}
            mono
            disabled={ctx.readonly}
            onChange={(event) =>
              setContract({
                ...document,
                contract: { ...document.contract, id: event.target.value },
              })
            }
          />
        </Field>
        <Field label="Contract version" required>
          <Input
            value={document.contract.version}
            mono
            disabled={ctx.readonly}
            onChange={(event) =>
              setContract({
                ...document,
                contract: { ...document.contract, version: event.target.value },
              })
            }
          />
        </Field>
        <Field label="IR version">
          <Input value={document.ir_version} mono disabled />
        </Field>
        <Field label="Input kind">
          <Input value={document.input.kind} mono disabled />
        </Field>
      </Section>
      <Section
        title="Rules"
        description="Add the checks this gate should apply. Clear records pass downstream; quarantined records are removed from the output."
      >
        {document.rules.map((rule, index) => (
          <RuleEditor
            key={`${rule.id}:${index}`}
            ctx={ctx}
            rule={rule}
            index={index}
            onChange={(next) => updateRule(index, next)}
            onRemove={() =>
              setContract({ ...document, rules: document.rules.filter((_, i) => i !== index) })
            }
          />
        ))}
        {!document.rules.length && (
          <Callout tone="info">No rules yet. Add a rule or import a contract below.</Callout>
        )}
        <Button
          size="sm"
          onClick={() =>
            setContract({
              ...document,
              rules: [
                ...document.rules,
                {
                  id: '',
                  kind: kindForPredicate('required'),
                  path: '$.',
                  predicate: { op: 'required' },
                  on_breach: { action: 'quarantine', severity: 'error' },
                },
              ],
            })
          }
          disabled={ctx.readonly}
        >
          Add rule
        </Button>
      </Section>
      <Section
        title="Optional JSON import"
        description="Paste or upload a canonical contract when you already have one. Invalid JSON is not written into the node."
      >
        <Field label="Contract JSON" error={importError || undefined}>
          <Textarea
            value={importText}
            mono
            rows={8}
            disabled={ctx.readonly}
            placeholder={JSON.stringify(DEFAULT_CONTRACT, null, 2)}
            onChange={(event) => setImportText(event.target.value)}
          />
        </Field>
        <div className="ps-form-actions">
          <Button
            size="sm"
            onClick={() => importContract(importText)}
            disabled={ctx.readonly || !importText.trim()}
          >
            Import JSON
          </Button>
          <label className="bk-button bk-button-sm">
            Upload JSON
            <input
              type="file"
              accept="application/json,.json"
              hidden
              disabled={ctx.readonly}
              onChange={readFile}
            />
          </label>
        </div>
      </Section>
    </>
  )
}
