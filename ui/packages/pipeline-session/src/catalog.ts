import { ArrowRightLeft, Bell, Boxes, Code2, Combine, Database, DatabaseBackup, FileCode2, FileDown, FileInput, Filter, Globe, Hourglass, Merge, Package, Puzzle, Send, ShieldCheck, Shuffle, Split, Waypoints, type LucideIcon } from 'lucide-react'
import type { PipelineNode } from '@brokoli/api'

export type Family = 'source' | 'processing' | 'output' | 'integration' | 'migration' | 'control'

export type CatalogEntry = {
  type: string
  label: string
  /** Icon for the node card and the palette. */
  glyph: LucideIcon
  description: string
  family: Family
  input: boolean
  output: boolean
  /** -1 means the editor sets no limit and leaves it to server validation. */
  maxInputs: number
  /** Palette group; entries without one render but are not offered for adding. */
  group?: PaletteGroup
}

export const PALETTE_GROUPS = ['Sources', 'Processing', 'Outputs', 'Extensions', 'Migration', 'Flow control'] as const
export type PaletteGroup = (typeof PALETTE_GROUPS)[number]

/*
 * Node types the server knows (models/pipeline.go). The port rules match
 * the Svelte editor (dag.ts nodePortConfig), which match server
 * validation: sources take no input, sinks produce no output, a join takes
 * exactly two inputs, migrate is standalone.
 */
export const CATALOG: CatalogEntry[] = [
  { type: 'source_file', label: 'File Source', glyph: FileInput, description: 'Read a CSV, JSON, XML or Excel file', family: 'source', input: false, output: true, maxInputs: 0, group: 'Sources' },
  { type: 'source_api', label: 'API Source', glyph: Globe, description: 'Fetch records from an HTTP API', family: 'source', input: false, output: true, maxInputs: 0, group: 'Sources' },
  { type: 'source_db', label: 'Database Source', glyph: Database, description: 'Run a SQL query against a database', family: 'source', input: false, output: true, maxInputs: 0, group: 'Sources' },
  { type: 'transform', label: 'Transform', glyph: Shuffle, description: 'Rename, filter, sort, aggregate and reshape rows', family: 'processing', input: true, output: true, maxInputs: 1, group: 'Processing' },
  { type: 'code', label: 'Code', glyph: Code2, description: 'Run a Python or TypeScript script over the rows', family: 'processing', input: true, output: true, maxInputs: 1, group: 'Processing' },
  { type: 'join', label: 'Join', glyph: Merge, description: 'Combine two inputs on a key column', family: 'processing', input: true, output: true, maxInputs: 2, group: 'Processing' },
  { type: 'quality_check', label: 'Quality Check', glyph: ShieldCheck, description: 'Assert rules about the data, then block or warn', family: 'processing', input: true, output: true, maxInputs: 1, group: 'Processing' },
  { type: 'sql_generate', label: 'SQL Generate', glyph: FileCode2, description: 'Turn rows into INSERT statements', family: 'processing', input: true, output: true, maxInputs: 1, group: 'Processing' },
  { type: 'sink_file', label: 'File Output', glyph: FileDown, description: 'Write rows to a CSV, JSON or SQL file', family: 'output', input: true, output: false, maxInputs: 1, group: 'Outputs' },
  { type: 'sink_db', label: 'Database Sink', glyph: DatabaseBackup, description: 'Write rows into a database table', family: 'output', input: true, output: false, maxInputs: 1, group: 'Outputs' },
  { type: 'sink_api', label: 'API Sink', glyph: Send, description: 'Send rows to an HTTP endpoint in batches', family: 'output', input: true, output: false, maxInputs: 1, group: 'Outputs' },
  { type: 'dbt', label: 'dbt', glyph: Boxes, description: 'Run a dbt command against a project', family: 'integration', input: false, output: true, maxInputs: 0, group: 'Extensions' },
  { type: 'notify', label: 'Notify', glyph: Bell, description: 'Post a message to Slack or a webhook', family: 'integration', input: true, output: false, maxInputs: 1, group: 'Extensions' },
  { type: 'migrate', label: 'DB Migration', glyph: ArrowRightLeft, description: 'Copy a table from one database to another', family: 'migration', input: false, output: false, maxInputs: 0, group: 'Migration' },
  { type: 'condition', label: 'If / Else', glyph: Split, description: 'Continue or skip downstream nodes based on the data', family: 'control', input: true, output: true, maxInputs: 1, group: 'Flow control' },
  // Authored through the SDKs; rendered and editable, not offered in the palette.
  { type: 'wait', label: 'Wait', glyph: Hourglass, description: 'Park the run until a file, URL, interval or pipeline is ready', family: 'control', input: true, output: true, maxInputs: -1 },
  { type: 'union', label: 'Union', glyph: Combine, description: 'Concatenate several inputs into one dataset', family: 'processing', input: true, output: true, maxInputs: -1 },
  { type: 'dataset_map', label: 'Dataset Map', glyph: Waypoints, description: 'Apply a function to every partition', family: 'processing', input: true, output: true, maxInputs: 1 },
  { type: 'dataset_filter', label: 'Dataset Filter', glyph: Filter, description: 'Keep the partitions a function selects', family: 'processing', input: true, output: true, maxInputs: 1 },
  { type: 'task', label: 'Task', glyph: Package, description: 'Run a packaged task bundle (Python, Node, JVM and more)', family: 'processing', input: true, output: true, maxInputs: -1 },
]

const BY_TYPE = new Map(CATALOG.map((e) => [e.type, e]))

function humanize(type: string) {
  return type.replaceAll('_', ' ').replace(/^\w/, (c) => c.toUpperCase())
}


/** Unknown types come from plugins or enterprise executors; the server decides what they accept. */
export function catalogEntry(type: string): CatalogEntry {
  return (
    BY_TYPE.get(type) ?? {
      type,
      label: humanize(type),
      glyph: Puzzle,
      description: 'Provided by a plugin or extension',
      family: 'integration',
      input: true,
      output: true,
      maxInputs: -1,
    }
  )
}

/** Ports for a concrete node. A task node takes input only when its interface declares one (ADR-032). */
export function portsFor(node: PipelineNode) {
  const entry = catalogEntry(node.type)
  if (node.type === 'task') {
    const inputs = (node.interface as { inputs?: Record<string, unknown> } | undefined)?.inputs
    const takesInput = Boolean(inputs && 'input' in inputs)
    return { input: takesInput, output: true, maxInputs: takesInput ? 1 : 0 }
  }
  return { input: entry.input, output: entry.output, maxInputs: entry.maxInputs }
}

export const FAMILY_COLOR: Record<Family, string> = {
  source: 'var(--bk-color-tax-source)',
  processing: 'var(--bk-color-tax-processing)',
  output: 'var(--bk-color-tax-output)',
  integration: 'var(--bk-color-tax-integration)',
  migration: 'var(--bk-color-tax-migration)',
  control: 'var(--bk-color-tax-control)',
}
