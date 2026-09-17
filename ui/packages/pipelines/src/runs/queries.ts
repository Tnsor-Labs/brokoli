import type { RunEvent } from '@brokoli/api'

// The exact SQL each node ran, recorded per attempt with variables already
// substituted. It arrives on the run events endpoint as `attempt.query` events,
// the statement on `payload.statement`. This module holds the pure shaping so it
// can be tested without a DOM.

// A sink node's statements are synthesized by the engine (the CREATE/DELETE/
// INSERT it builds to load rows), not written by the user, so the SQL panel
// hides them. This is an interim heuristic for brokoli#667, where the recorder
// itself will stop recording engine-generated writes; until then the node type
// is the only signal the UI has.
export function isGeneratedSqlNode(nodeType: string | undefined): boolean {
  return (nodeType ?? '').startsWith('sink_')
}

export type StatementKind = 'sql' | 'withheld' | 'none'

// A recorded statement is one of four things, and two of them are not errors:
//  - a statement (possibly with a visible truncation marker appended),
//  - a withheld statement (a secret it substitutes was too short to mask safely),
//  - no statement by nature (a bulk path such as COPY / LOAD DATA streams rows).
// Every non-statement case is emitted as a `-- [brokoli]` SQL comment.
export function classifyStatement(statement: string): { kind: StatementKind; truncated: boolean } {
  const head = statement.trimStart()
  if (head.startsWith('-- [brokoli] no SQL statement')) return { kind: 'none', truncated: false }
  if (head.startsWith('-- [brokoli] statement not recorded')) return { kind: 'withheld', truncated: false }
  // A truncated statement is a real statement with the marker on its own line;
  // it stays visible, we only note it.
  return { kind: 'sql', truncated: statement.includes('-- [brokoli] statement truncated:') }
}

// For a withheld / no-statement comment, drop the `-- [brokoli] <label>:` prefix
// and surface the plain reason; keep the raw text if the shape is unexpected.
export function statementReason(statement: string): string {
  const reason = statement.replace(/^\s*--\s*\[brokoli\]\s*(?:no SQL statement:|statement not recorded:)\s*/, '').trim()
  return reason || statement
}

export type QueryStatement = { id: RunEvent['id']; statement: string }
export type QueryGroup = { nodeId?: string; attempt: number; statements: QueryStatement[] }

function statementOf(e: RunEvent): string {
  const s = e.payload?.statement
  return typeof s === 'string' ? s : ''
}

// Events sort by id; it arrives as a number but tolerate a string id too.
function byId(a: RunEvent['id'], b: RunEvent['id']): number {
  if (typeof a === 'number' && typeof b === 'number') return a - b
  return String(a).localeCompare(String(b), undefined, { numeric: true })
}

// Group the query events by node and attempt, keeping statements in recorded
// order. A single attempt can record several (a pushed-down overwrite is the
// clear plus the refill), so a group holds a list, never a single statement.
export function groupQueries(events: RunEvent[]): QueryGroup[] {
  const queries = events
    .filter((e) => e.event_type === 'attempt.query')
    .slice()
    .sort((a, b) => byId(a.id, b.id))
  const byKey = new Map<string, QueryGroup>()
  for (const e of queries) {
    const attempt = e.attempt ?? 0
    const key = `${e.node_id ?? ''}::${attempt}`
    let group = byKey.get(key)
    if (!group) {
      group = { nodeId: e.node_id, attempt, statements: [] }
      byKey.set(key, group)
    }
    group.statements.push({ id: e.id, statement: statementOf(e) })
  }
  return [...byKey.values()]
}
