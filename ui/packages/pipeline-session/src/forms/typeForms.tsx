import type { ComponentType } from 'react'
import { Callout, Field, Input } from '@brokoli/ui'
import { isConditionSupported } from '../document'
import {
  CheckField,
  ConnectionField,
  LegacyKeyNotice,
  ListField,
  MapField,
  NumberField,
  ScriptField,
  Section,
  SelectField,
  TestConnection,
  TextField,
  str,
  type FormCtx,
  type TypeFormProps,
} from './fields'
import { QualityRules } from './QualityRules'
import { TransformRules } from './TransformRules'

/** Connection types the engine can build a database URI for (models/connection.go BuildsURI). */
const DB_TYPES = ['postgres', 'redshift', 'mysql', 'sqlite', 'mssql', 'snowflake', 'clickhouse']
const HTTP_TYPES = ['http']
const DBT_TYPES = ['postgres', 'mysql', 'clickhouse']
const DIALECTS = [
  { value: 'postgres', label: 'PostgreSQL' },
  { value: 'mysql', label: 'MySQL' },
  { value: 'sqlite', label: 'SQLite' },
  { value: 'sqlserver', label: 'SQL Server' },
  { value: 'clickhouse', label: 'ClickHouse' },
  { value: 'generic', label: 'Generic SQL' },
]
const WRITE_MODES = [
  { value: 'append', label: 'Append rows' },
  { value: 'overwrite', label: 'Replace the table contents' },
  { value: 'upsert', label: 'Upsert on key columns' },
]

function DatabaseTarget({ ctx, uriPlaceholder = 'postgres://user:pass@host:5432/db' }: { ctx: FormCtx; uriPlaceholder?: string }) {
  const conn = str(ctx.get('conn_id'))
  return (
    <>
      <ConnectionField ctx={ctx} uriKey="uri" types={DB_TYPES} noneLabel="Enter a connection URI instead" />
      {!conn && <TextField ctx={ctx} name="uri" label="Connection URI" mono placeholder={uriPlaceholder} required hint="Stored in the pipeline as written. Prefer a saved connection for credentials." />}
      <TestConnection connId={conn || undefined} uri={str(ctx.get('uri')) || undefined} />
    </>
  )
}

function SourceFile({ ctx }: TypeFormProps) {
  return (
    <Section title="File">
      <TextField ctx={ctx} name="path" label="File path" mono required placeholder="/data/input.csv" hint="The format comes from the extension: .csv, .json, .xml, .xlsx or .xls. The path must be inside the server's data directories." />
      <LegacyKeyNotice ctx={ctx} name="format">
        The engine picks the reader from the file extension and ignores this setting.
      </LegacyKeyNotice>
    </Section>
  )
}

function SourceApi({ ctx }: TypeFormProps) {
  const method = str(ctx.get('method')) || 'GET'
  const response = str(ctx.get('response')) || 'dataset'
  const legacy = str(ctx.get('response_path'))
  return (
    <>
      <Section title="Request">
        <ConnectionField ctx={ctx} types={HTTP_TYPES} noneLabel="No connection (full URL below)" hint="A connection supplies the base URL, headers and credentials." />
        <TextField ctx={ctx} name="url" label="URL" mono required placeholder={ctx.get('conn_id') ? '/v1/orders' : 'https://api.example.com/v1/orders'} hint={ctx.get('conn_id') ? "A path starting with / is appended to the connection's base URL." : undefined} />
        <SelectField ctx={ctx} name="method" label="Method" options={['GET', 'POST', 'PUT', 'PATCH', 'DELETE']} defaultLabel="GET" />
        {['POST', 'PUT', 'PATCH'].includes(method) && <TextField ctx={ctx} name="body" label="Request body" multiline mono rows={5} placeholder='{"since": "${interval.start}"}' hint="Sent as written. Set a Content-Type header if the API needs one." />}
        <MapField ctx={ctx} name="params" label="Query parameters" addLabel="Add a parameter" />
        <MapField ctx={ctx} name="headers" label="Headers" addLabel="Add a header" keyPlaceholder="Header-Name" />
      </Section>
      <Section title="Response">
        <SelectField
          ctx={ctx}
          name="response"
          label="Treat the response as"
          defaultLabel="a dataset of records"
          options={[
            { value: 'dataset', label: 'A dataset of records' },
            { value: 'scalar', label: 'A single value' },
            { value: 'artifact', label: 'A file artifact' },
          ]}
        />
        {response === 'dataset' && <TextField ctx={ctx} name="records" label="Records path" mono placeholder="data.items" hint="Dot path to the array of records. Leave empty when the body is the array." />}
        {response === 'scalar' && <TextField ctx={ctx} name="value_path" label="Value path" mono placeholder="meta.total" />}
        <LegacyKeyNotice ctx={ctx} name="response_path" fix={{ label: 'Move it to Records path', patch: { records: legacy, response_path: undefined } }}>
          The engine reads the record location from "Records path". The old setting "{legacy}" is ignored.
        </LegacyKeyNotice>
      </Section>
    </>
  )
}

function SourceDb({ ctx }: TypeFormProps) {
  return (
    <Section title="Query">
      <DatabaseTarget ctx={ctx} />
      <ScriptField ctx={ctx} name="query" label="SQL query" language="sql" required />
    </Section>
  )
}

function Transform({ ctx }: TypeFormProps) {
  return (
    <Section title="Rules" description="Applied in order to every row.">
      <TransformRules ctx={ctx} />
    </Section>
  )
}

function Code({ ctx }: TypeFormProps) {
  const language = str(ctx.get('language')) || 'python'
  const bundle = ctx.get('task_bundle') as { digest?: string } | undefined
  return (
    <>
      <Section title="Script">
        <SelectField
          ctx={ctx}
          name="language"
          label="Language"
          defaultLabel="Python"
          options={[
            { value: 'python', label: 'Python' },
            { value: 'typescript', label: 'TypeScript' },
          ]}
        />
        {bundle ? (
          <Callout tone="info" title="Runs a task bundle">
            This node runs the packaged bundle {bundle.digest ? <code>{bundle.digest.slice(0, 19)}</code> : ''} published by an SDK. A script cannot be added alongside it.
          </Callout>
        ) : (
          <ScriptField ctx={ctx} name="script" label="Script" language={language === 'typescript' ? 'typescript' : 'python'} required />
        )}
        {language === 'typescript' ? (
          <TextField ctx={ctx} name="node_path" label="Node.js executable" mono placeholder="Detected automatically (Node 20 or newer)" />
        ) : (
          <TextField ctx={ctx} name="python_path" label="Python executable" mono placeholder="python3, or /path/to/venv/bin/python" />
        )}
      </Section>
      <Section title="Limits">
        <NumberField ctx={ctx} name="timeout" label="Timeout (seconds)" defaultValue={30} min={1} />
        <NumberField ctx={ctx} name="max_memory_mb" label="Memory limit (MB)" defaultValue="server limit" min={1} />
        <NumberField ctx={ctx} name="max_cpu_seconds" label="CPU limit (seconds)" defaultValue="server limit" min={1} />
      </Section>
    </>
  )
}

function Join({ ctx, nodes, edges }: TypeFormProps) {
  const inputs = edges.filter((e) => e.to === ctx.node.id).map((e) => nodes.find((n) => n.id === e.from)?.name ?? e.from)
  return (
    <Section title="Join" description={inputs.length === 2 ? `Left: ${inputs[0]}. Right: ${inputs[1]}. The first connection made is the left side.` : 'Connect exactly two inputs. The first connection made is the left side.'}>
      <SelectField
        ctx={ctx}
        name="join_type"
        label="Join type"
        defaultLabel="inner"
        options={[
          { value: 'inner', label: 'Inner (matching rows only)' },
          { value: 'left', label: 'Left (all left rows)' },
          { value: 'right', label: 'Right (all right rows)' },
          { value: 'full', label: 'Full (all rows)' },
        ]}
      />
      <TextField ctx={ctx} name="left_key" label="Left key column" mono required placeholder="customer_id" />
      <TextField ctx={ctx} name="right_key" label="Right key column" mono placeholder="Same as the left key" hint="Right-side columns with a clashing name get a right_ prefix." />
    </Section>
  )
}

function QualityCheck({ ctx }: TypeFormProps) {
  return (
    <Section title="Checks">
      <SelectField
        ctx={ctx}
        name="on_failure"
        label="Default when a check fails"
        defaultLabel="warn and continue"
        options={[
          { value: 'block', label: 'Block the run' },
          { value: 'warn', label: 'Warn and continue' },
        ]}
      />
      <QualityRules ctx={ctx} />
    </Section>
  )
}

function SqlGenerate({ ctx }: TypeFormProps) {
  return (
    <Section title="SQL output" description="Produces one row with a sql_output column; connect it to a File Output (SQL) or a Database Sink.">
      <TextField ctx={ctx} name="table" label="Table name" mono required />
      <SelectField ctx={ctx} name="dialect" label="Dialect" options={DIALECTS} defaultLabel="Generic SQL" />
      <NumberField ctx={ctx} name="batch_size" label="Rows per INSERT" defaultValue={100} min={1} />
      <CheckField ctx={ctx} name="create_table" label="Include CREATE TABLE IF NOT EXISTS" />
    </Section>
  )
}

function inferFileFormat(path: string) {
  const p = path.toLowerCase()
  if (p.endsWith('.csv') || p.endsWith('.tsv')) return 'csv'
  if (p.endsWith('.sql')) return 'sql'
  return 'json'
}

function SinkFile({ ctx }: TypeFormProps) {
  const path = str(ctx.get('path'))
  const inferred = inferFileFormat(path)
  const effective = str(ctx.get('format')) || inferred
  return (
    <Section title="File">
      <TextField ctx={ctx} name="path" label="Output path" mono required placeholder="/output/result.csv" />
      <SelectField ctx={ctx} name="format" label="Format" options={['csv', 'json', 'sql']} defaultLabel={`from the extension (${inferred})`} />
      {path.toLowerCase().endsWith('.tsv') && <p className="ps-form-warning">.tsv files are written comma-separated.</p>}
      {effective === 'sql' && (
        <>
          <TextField ctx={ctx} name="table" label="Table name" mono placeholder="Taken from the file name" />
          <SelectField ctx={ctx} name="dialect" label="Dialect" options={DIALECTS} defaultLabel="Generic SQL" />
          <NumberField ctx={ctx} name="batch_size" label="Rows per INSERT" defaultValue={100} min={1} />
          <CheckField ctx={ctx} name="create_table" label="Include CREATE TABLE IF NOT EXISTS" />
        </>
      )}
    </Section>
  )
}

function SinkDb({ ctx, nodes, edges }: TypeFormProps) {
  const fromSqlGenerate = edges.some((e) => e.to === ctx.node.id && nodes.find((n) => n.id === e.from)?.type === 'sql_generate')
  const mode = str(ctx.get('mode')) || 'append'
  return (
    <>
      <Section title="Destination">
        <DatabaseTarget ctx={ctx} />
        <TextField
          ctx={ctx}
          name="table"
          label="Table"
          mono
          required={!fromSqlGenerate}
          placeholder="analytics.orders"
          hint={fromSqlGenerate ? 'Optional: the upstream SQL Generate node supplies the statements.' : 'schema.table is allowed.'}
        />
      </Section>
      <Section title="Write behaviour">
        <SelectField ctx={ctx} name="mode" label="Mode" options={WRITE_MODES} defaultLabel="append rows" />
        {mode === 'upsert' && <ListField ctx={ctx} name="key_columns" label="Key columns" required placeholder="id" hint="Rows with the same key are updated instead of inserted. ClickHouse does not support upsert." />}
        {mode === 'overwrite' && <CheckField ctx={ctx} name="truncate" label="Use TRUNCATE" description="Faster than DELETE on large tables." />}
        <CheckField ctx={ctx} name="create_table" label="Create the table if it does not exist" />
        <TextField ctx={ctx} name="table_engine" label="Table engine (ClickHouse)" mono placeholder="MergeTree ORDER BY id" />
      </Section>
    </>
  )
}

function SinkApi({ ctx }: TypeFormProps) {
  return (
    <Section title="Request" description="Rows are sent as JSON arrays, one request per batch. Any HTTP status of 400 or above fails the node.">
      <ConnectionField ctx={ctx} types={HTTP_TYPES} noneLabel="No connection (full URL below)" />
      <TextField ctx={ctx} name="url" label="URL" mono required placeholder="https://api.example.com/ingest" />
      <SelectField ctx={ctx} name="method" label="Method" options={['POST', 'PUT', 'PATCH']} defaultLabel="POST" />
      <NumberField ctx={ctx} name="batch_size" label="Rows per request" defaultValue={100} min={1} />
      <MapField ctx={ctx} name="headers" label="Headers" addLabel="Add a header" keyPlaceholder="Header-Name" hint="Content-Type: application/json is always sent." />
    </Section>
  )
}

function Migrate({ ctx }: TypeFormProps) {
  const mode = str(ctx.get('mode')) || 'append'
  return (
    <>
      <Section title="Source">
        <ConnectionField ctx={ctx} idKey="source_conn_id" uriKey="source_uri" types={DB_TYPES} label="Source connection" noneLabel="Enter a source URI instead" />
        {!ctx.get('source_conn_id') && <TextField ctx={ctx} name="source_uri" label="Source URI" mono required />}
        <ScriptField ctx={ctx} name="source_query" label="Source query" language="sql" required />
      </Section>
      <Section title="Destination">
        <ConnectionField ctx={ctx} idKey="dest_conn_id" uriKey="dest_uri" types={DB_TYPES} label="Destination connection" noneLabel="Enter a destination URI instead" />
        {!ctx.get('dest_conn_id') && <TextField ctx={ctx} name="dest_uri" label="Destination URI" mono required />}
        <TextField ctx={ctx} name="dest_table" label="Destination table" mono required />
        <SelectField ctx={ctx} name="dialect" label="Dialect" options={DIALECTS} defaultLabel="inferred from the destination" />
        <SelectField ctx={ctx} name="mode" label="Mode" options={WRITE_MODES} defaultLabel="append rows" />
        {mode === 'upsert' && <ListField ctx={ctx} name="key_columns" label="Key columns" required />}
        <NumberField ctx={ctx} name="chunk_size" label="Rows per chunk" defaultValue={5000} min={1} />
        <CheckField ctx={ctx} name="create_table" label="Create the table if it does not exist" />
      </Section>
    </>
  )
}

const CONDITION_EXAMPLES = ['row_count > 0', 'column_exists("email")', 'null_pct("email") < 5', 'max("amount") <= 10000', 'always_true']

function Condition({ ctx }: TypeFormProps) {
  const expr = str(ctx.get('expression'))
  const supported = !expr || isConditionSupported(expr)
  return (
    <Section title="Condition" description="Connections leaving this node are marked true or false. Downstream nodes on the branch that does not match are skipped.">
      <Field
        label="Expression"
        error={supported ? undefined : 'Not in the supported grammar. Under IR 2.1 the server rejects it; under IR 2.0 it silently evaluates to false.'}
        hint={!expr ? 'Empty passes everything through as true.' : supported ? 'Supported expression.' : undefined}
      >
        <Input mono value={expr} placeholder="row_count > 0" onChange={(e) => ctx.set({ expression: e.target.value }, `field:${ctx.node.id}:expression`)} />
      </Field>
      <div className="ps-examples">
        {CONDITION_EXAMPLES.map((ex) => (
          <button key={ex} type="button" onClick={() => ctx.set({ expression: ex })} disabled={ctx.readonly}>
            {ex}
          </button>
        ))}
      </div>
    </Section>
  )
}

function Dbt({ ctx }: TypeFormProps) {
  const conn = str(ctx.get('conn_id'))
  return (
    <>
      <Section title="Command">
        <SelectField ctx={ctx} name="command" label="Command" options={['run', 'test', 'build', 'seed', 'snapshot', 'compile', 'ls']} defaultLabel="run" />
        <TextField ctx={ctx} name="select" label="Select models" mono placeholder="All models" hint="dbt selection syntax, passed as --select." />
        <TextField ctx={ctx} name="project_dir" label="Project directory" mono placeholder="." />
      </Section>
      <Section title="Warehouse">
        <ConnectionField ctx={ctx} types={DBT_TYPES} noneLabel="Use the project's own profiles.yml" hint="With a connection, Brokoli writes the dbt profile for you." />
        {conn ? (
          <>
            <TextField ctx={ctx} name="target_schema" label="Schema to build into" mono required />
            <TextField ctx={ctx} name="output_model" label="Output model" mono placeholder="Optional" hint="Hands this model's table to downstream nodes." />
          </>
        ) : (
          <TextField ctx={ctx} name="target" label="Profile target" mono placeholder="The profile's default" />
        )}
        {conn && ctx.get('profiles_dir') !== undefined && <p className="ps-form-warning">A profiles directory is set; it takes precedence over the connection.</p>}
      </Section>
      <Section title="Advanced">
        <TextField ctx={ctx} name="profiles_dir" label="Profiles directory" mono />
        <TextField ctx={ctx} name="profile" label="Profile name" mono placeholder="From dbt_project.yml" />
        <NumberField ctx={ctx} name="threads" label="Threads" defaultValue={4} min={1} />
        <TextField ctx={ctx} name="vars" label="Variables" multiline mono rows={3} placeholder="{key: value}" hint="YAML or JSON, passed as --vars." />
      </Section>
    </>
  )
}

function Notify({ ctx }: TypeFormProps) {
  const slack = str(ctx.get('notify_type')) === 'slack'
  return (
    <Section title="Notification">
      <SelectField
        ctx={ctx}
        name="notify_type"
        label="Send via"
        defaultLabel="webhook"
        options={[
          { value: 'webhook', label: 'Webhook' },
          { value: 'slack', label: 'Slack' },
        ]}
      />
      <TextField ctx={ctx} name="webhook_url" label={slack ? 'Slack webhook URL' : 'Webhook URL'} mono required />
      {slack && <TextField ctx={ctx} name="channel" label="Channel" mono placeholder="#data-alerts" />}
      <TextField ctx={ctx} name="message" label="Message" multiline rows={3} placeholder="Pipeline {{pipeline}} completed" hint="{{pipeline}}, {{run_id}} and {{rows}} are filled in." />
    </Section>
  )
}

function Wait({ ctx }: TypeFormProps) {
  const condition = str(ctx.get('condition'))
  return (
    <Section title="Wait for" description="The run is parked, without holding a run slot, until the condition is met or the timeout passes.">
      <SelectField
        ctx={ctx}
        name="condition"
        label="Condition"
        required
        options={[
          { value: 'file_exists', label: 'A file exists' },
          { value: 'http', label: 'A URL answers' },
          { value: 'interval_elapsed', label: 'The interval has ended' },
          { value: 'pipeline', label: 'Another pipeline succeeded' },
        ]}
      />
      {condition === 'file_exists' && <TextField ctx={ctx} name="path" label="Path" mono required />}
      {condition === 'http' && (
        <>
          <TextField ctx={ctx} name="url" label="URL" mono required />
          <NumberField ctx={ctx} name="expect_status" label="Expected status" defaultValue={200} />
        </>
      )}
      {condition === 'interval_elapsed' && <TextField ctx={ctx} name="offset" label="Extra delay" mono placeholder="15m" />}
      {condition === 'pipeline' && <TextField ctx={ctx} name="pipeline_id" label="Pipeline id" mono required />}
      <TextField ctx={ctx} name="poll_interval" label="Check every" mono placeholder="30s" hint="A duration such as 30s, 5m or 1h." />
      <TextField ctx={ctx} name="timeout" label="Give up after" mono placeholder="6h" hint="A duration. This is the wait timeout, not a run attempt timeout." />
    </Section>
  )
}

function Union({ ctx }: TypeFormProps) {
  return (
    <Section title="Union">
      <p className="ps-form-note">Concatenates every connected input into one dataset. Connect at least two inputs, or one from a fan-out node.</p>
      {str(ctx.get('mode')) && str(ctx.get('mode')) !== 'union' && <p className="ps-form-warning">Unsupported mode "{str(ctx.get('mode'))}".</p>}
    </Section>
  )
}

function Task({ ctx }: TypeFormProps) {
  const bundle = ctx.get('task_bundle') as { digest?: string; format?: string } | undefined
  return (
    <>
      <Section title="Task bundle" description="Task nodes are authored and published with the Python or TypeScript SDK.">
        {bundle?.digest ? (
          <dl className="ps-facts">
            <dt>Digest</dt>
            <dd className="bk-mono">{bundle.digest}</dd>
            <dt>Format</dt>
            <dd className="bk-mono">{bundle.format ?? 'unknown'}</dd>
          </dl>
        ) : (
          <Callout tone="danger">This task node has no bundle and cannot run.</Callout>
        )}
        {ctx.node.interface && <pre className="ps-json-preview">{JSON.stringify(ctx.node.interface, null, 2)}</pre>}
      </Section>
      <Section title="Limits">
        <NumberField ctx={ctx} name="timeout" label="Timeout (seconds)" defaultValue={30} min={1} />
        <NumberField ctx={ctx} name="max_memory_mb" label="Memory limit (MB)" defaultValue="server limit" min={1} />
        <NumberField ctx={ctx} name="max_cpu_seconds" label="CPU limit (seconds)" defaultValue="server limit" min={1} />
      </Section>
    </>
  )
}

function DatasetFunction({ ctx }: TypeFormProps) {
  const fn = (ctx.get('function') as { name?: string; script?: string } | undefined) ?? {}
  const setFn = (patch: Record<string, unknown>, key?: string) => ctx.set({ function: Object.fromEntries(Object.entries({ ...fn, ...patch }).filter(([, v]) => v !== undefined && v !== '')) }, key)
  return (
    <Section title="Function">
      <Field label="Function name" required error={!fn.name ? 'Required' : undefined}>
        <Input mono value={fn.name ?? ''} onChange={(e) => setFn({ name: e.target.value }, `field:${ctx.node.id}:fn`)} />
      </Field>
      <ScriptField
        ctx={{ ...ctx, get: (k) => (k === '__fn_script' ? fn.script : ctx.get(k)), set: (patch) => setFn({ script: patch.__fn_script }) }}
        name="__fn_script"
        label="Function script"
        language="python"
        required
      />
      <NumberField ctx={ctx} name="timeout" label="Timeout (seconds)" defaultValue={30} min={1} />
    </Section>
  )
}

export const TYPE_FORMS: Record<string, ComponentType<TypeFormProps>> = {
  source_file: SourceFile,
  source_api: SourceApi,
  source_db: SourceDb,
  transform: Transform,
  code: Code,
  join: Join,
  quality_check: QualityCheck,
  sql_generate: SqlGenerate,
  sink_file: SinkFile,
  sink_db: SinkDb,
  sink_api: SinkApi,
  migrate: Migrate,
  condition: Condition,
  dbt: Dbt,
  notify: Notify,
  wait: Wait,
  union: Union,
  task: Task,
  dataset_map: DatasetFunction,
  dataset_filter: DatasetFunction,
}

/** Types whose own form owns `timeout` (a script timeout or a duration), so the shared execution section must not show one. */
export const OWNS_TIMEOUT = new Set(['code', 'task', 'wait', 'dataset_map', 'dataset_filter'])
