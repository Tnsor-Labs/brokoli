import { describe, expect, it } from 'vitest'
import type { Pipeline, PipelineEdge, PipelineNode } from '@brokoli/api'
import { catalogEntry, portsFor } from './catalog'
import {
  autoLayout,
  buildSavePayload,
  connectionProblem,
  duplicateNode,
  irVersionFor,
  isConditionSupported,
  newNodeId,
  nextFreePosition,
  nodeWarnings,
  NODE_HEIGHT,
  NODE_WIDTH,
  patchConfig,
  sanitizeTags,
  joinOutputSchema,
  outputSchemaForNode,
  transformOutputSchema,
} from './document'

const node = (id: string, type: string, extra: Partial<PipelineNode> = {}): PipelineNode => ({
  id,
  type,
  name: id,
  config: {},
  position: { x: 0, y: 0 },
  ...extra,
})

const pipeline = (extra: Partial<Pipeline> = {}): Pipeline => ({
  id: 'p1',
  name: 'p',
  description: '',
  nodes: [],
  edges: [],
  schedule: '',
  enabled: true,
  ...extra,
})

describe('save payload', () => {
  it('keeps fields the editor does not model, on the pipeline, nodes and edges', () => {
    const task = node('t', 'task', {
      interface: { inputs: { input: { kind: 'dataset' } } },
      capabilities: ['compute'],
    })
    const edge: PipelineEdge = { from: 's', to: 't', from_port: 'out', to_port: 'input' }
    const base = pipeline({
      parameters: { limit: { type: 'int' } },
      extensions: { 'x.acme': { a: 1 } },
      draft: true,
      hooks: { on_failure: {} },
    })
    const payload = buildSavePayload(base, [node('s', 'source_file'), task], [edge])
    expect(payload.parameters).toEqual({ limit: { type: 'int' } })
    expect(payload.extensions).toEqual({ 'x.acme': { a: 1 } })
    expect(payload.draft).toBe(true)
    expect(payload.hooks).toEqual({ on_failure: {} })
    expect(payload.nodes[1].interface).toEqual({ inputs: { input: { kind: 'dataset' } } })
    expect(payload.nodes[1].capabilities).toEqual(['compute'])
    expect(payload.edges[0]).toEqual(edge)
  })

  it('adds no keys the server does not know', () => {
    const payload = buildSavePayload(pipeline(), [node('a', 'source_file')], [])
    const allowedNode = ['id', 'type', 'name', 'config', 'position', 'capabilities', 'interface']
    for (const n of payload.nodes)
      expect(Object.keys(n).every((k) => allowedNode.includes(k))).toBe(true)
    expect(Object.keys(payload).sort()).toEqual(Object.keys(pipeline()).sort())
  })

  it('does not share structure with the editor state', () => {
    const nodes = [node('a', 'source_file')]
    const payload = buildSavePayload(pipeline(), nodes, [])
    expect(payload.nodes).toBe(nodes)
    expect(payload).not.toBe(pipeline())
  })
})

describe('IR version', () => {
  const branch: PipelineEdge[] = [{ from: 'c', to: 'x', condition: true }]
  it('leaves the version alone without conditional edges', () => {
    expect(irVersionFor({ ir_version: '2.2' }, [{ from: 'a', to: 'b' }])).toEqual({
      version: '2.2',
    })
    expect(irVersionFor({}, [])).toEqual({ version: undefined })
  })
  it('upgrades unset and 2.0 to 2.1 for branches', () => {
    expect(irVersionFor({}, branch)).toEqual({ version: '2.1' })
    expect(irVersionFor({ ir_version: '2.0' }, branch)).toEqual({ version: '2.1' })
    expect(irVersionFor({ ir_version: '2.1' }, branch)).toEqual({ version: '2.1' })
  })
  it('refuses to silently downgrade a 2.2 pipeline', () => {
    expect(irVersionFor({ ir_version: '2.2' }, branch).error).toMatch(/2\.2/)
    expect(() => buildSavePayload(pipeline({ ir_version: '2.2' }), [], branch)).toThrow(/2\.2/)
  })
  it('drops an unset version from the payload instead of sending undefined', () => {
    expect('ir_version' in buildSavePayload(pipeline(), [], [])).toBe(false)
  })
})

describe('connections', () => {
  const nodes = [
    node('src', 'source_db'),
    node('t1', 'transform'),
    node('t2', 'transform'),
    node('j', 'join'),
    node('sink', 'sink_db'),
    node('m', 'migrate'),
  ]
  it('allows a valid connection', () => {
    expect(connectionProblem(nodes, [], 'src', 't1')).toBeNull()
  })
  it('refuses self loops, duplicates and missing ports', () => {
    expect(connectionProblem(nodes, [], 't1', 't1')).toMatch(/itself/)
    expect(connectionProblem(nodes, [{ from: 'src', to: 't1' }], 'src', 't1')).toMatch(/already/)
    expect(connectionProblem(nodes, [], 'sink', 't1')).toMatch(/no output/)
    expect(connectionProblem(nodes, [], 't1', 'src')).toMatch(/does not take input/)
    expect(connectionProblem(nodes, [], 'm', 't1')).toMatch(/no output/)
  })
  it('enforces input limits, including the join maximum of two', () => {
    expect(connectionProblem(nodes, [{ from: 'src', to: 't1' }], 't2', 't1')).toMatch(
      /already has its input/,
    )
    const two = [
      { from: 'src', to: 'j' },
      { from: 't1', to: 'j' },
    ]
    expect(connectionProblem(nodes, two, 't2', 'j')).toMatch(/at most 2/)
  })
  it('refuses cycles', () => {
    expect(connectionProblem(nodes, [{ from: 't1', to: 't2' }], 't2', 't1')).toMatch(
      /already has|loop/,
    )
    const unlimited = [node('u', 'union'), node('v', 'union')]
    expect(connectionProblem(unlimited, [{ from: 'u', to: 'v' }], 'v', 'u')).toMatch(/loop/)
  })
  it('lets unknown plugin types connect and leaves limits to the server', () => {
    const plugin = [node('a', 'acme_thing'), node('b', 'acme_thing')]
    expect(catalogEntry('acme_thing').maxInputs).toBe(-1)
    expect(connectionProblem(plugin, [], 'a', 'b')).toBeNull()
  })
  it('derives task input from its declared interface', () => {
    expect(portsFor(node('t', 'task')).input).toBe(false)
    expect(portsFor(node('t', 'task', { interface: { inputs: { input: {} } } })).input).toBe(true)
  })
})

describe('node helpers', () => {
  it('generates ids that never collide with existing ones', () => {
    const taken = new Set<string>()
    for (let i = 0; i < 500; i++) taken.add(newNodeId(taken))
    expect(taken.size).toBe(500)
  })
  it('duplicates keep interface and capabilities', () => {
    const copy = duplicateNode(
      node('a', 'task', { interface: { x: 1 }, capabilities: ['source'] }),
      ['a'],
    )
    expect(copy.id).not.toBe('a')
    expect(copy.interface).toEqual({ x: 1 })
    expect(copy.capabilities).toEqual(['source'])
    expect(copy.name).toBe('a (copy)')
  })
  it('removes keys set to empty or undefined, keeps numbers and false', () => {
    expect(
      patchConfig(
        { python_path: '/usr/bin/python', a: 1 },
        { python_path: '', b: false, c: 0, a: undefined },
      ),
    ).toEqual({ b: false, c: 0 })
  })
  it('dedupes and trims tags', () => {
    expect(sanitizeTags([' a', 'a', '', 'b '])).toEqual(['a', 'b'])
  })
})

describe('condition grammar', () => {
  it.each([
    'always_true',
    'row_count > 0',
    'row_count>=10',
    'column_exists("id")',
    'null_pct("email") < 5.5',
    'max("amount") <= .5',
  ])('accepts %s', (e) => expect(isConditionSupported(e)).toBe(true))
  it.each(['row_count > 1.5', 'rows > 0', "column_exists('id')", 'min(amount) > 1', ''])(
    'rejects %s',
    (e) => expect(isConditionSupported(e)).toBe(false),
  )
  it('warns about unsupported expressions and unlabelled branches', () => {
    const c = node('c', 'condition', { config: { expression: 'rows > 1' } })
    expect(nodeWarnings(c, [{ from: 'c', to: 'x' }])).toHaveLength(2)
    expect(nodeWarnings(node('j', 'join'), [{ from: 'a', to: 'j' }])[0]).toMatch(/exactly 2/)
  })
})

describe('declared join schemas', () => {
  const schema = (names: string[]) => ({
    columns: names.map((name) => ({ name, type: { kind: 'string' } })),
    additional_columns: 'closed',
  })
  it('previews prefix, alias, and shared join-key output names', () => {
    const result = joinOutputSchema(schema(['id', 'name']), schema(['id', 'name', 'region']), {
      left_key: 'id',
      right_key: 'id',
      collision_policy: 'prefix',
    })
    expect(result.columns?.map((column) => column.name)).toEqual([
      'id',
      'name',
      'right_name',
      'right_region',
    ])
    expect(
      joinOutputSchema(schema(['id']), schema(['id', 'name']), {
        left_key: 'id',
        collision_policy: 'alias',
        right_alias: 'customer',
      }).columns?.map((column) => column.name),
    ).toEqual(['id', 'customer_name'])
  })
  it('reports missing keys and rejected collisions', () => {
    expect(
      joinOutputSchema(schema(['id', 'name']), schema(['id', 'name']), {
        left_key: 'id',
        right_key: 'id',
        collision_policy: 'error',
      }).error,
    ).toMatch(/Collision|rejects|columns/i)
    expect(
      joinOutputSchema(schema(['id']), schema(['user_id']), {
        left_key: 'missing',
        right_key: 'user_id',
      }).error,
    ).toMatch(/Left key/)
  })
  it('resolves a declared join output through upstream nodes', () => {
    const nodes = [
      node('left', 'source_file', { config: { schema: schema(['id']) } }),
      node('right', 'source_file', { config: { schema: schema(['id', 'value']) } }),
      node('join', 'join', { config: { left_key: 'id', right_key: 'id' } }),
    ]
    expect(
      outputSchemaForNode('join', nodes, [
        { from: 'left', to: 'join' },
        { from: 'right', to: 'join' },
      ])?.columns.map((column) => column.name),
    ).toEqual(['id', 'value'])
  })
})

describe('transform output schemas', () => {
  const schema = { columns: [{ name: 'id', type: { kind: 'int64' } }, { name: 'price', type: { kind: 'decimal', precision: 12, scale: 2 } }, { name: 'status', type: { kind: 'string' } }], additional_columns: 'closed' }
  it('carries schema through rename, drop, and add-column rules', () => {
    const result = transformOutputSchema(schema, { rules: [{ type: 'rename_columns', mapping: { status: 'state' } }, { type: 'drop_columns', columns: ['id'] }, { type: 'add_column', name: 'total', expression: 'price * price' }] })
    expect(result?.columns).toEqual([{ name: 'price', type: { kind: 'decimal', precision: 12, scale: 2 } }, { name: 'state', type: { kind: 'string' } }, { name: 'total', type: { kind: 'decimal', precision: 12, scale: 2 } }])
  })
  it('propagates a transform schema into a downstream join', () => {
    const nodes = [node('left', 'source_file', { config: { schema } }), node('transform', 'transform', { config: { rules: [{ type: 'rename_columns', mapping: { id: 'user_id' } }] } }), node('right', 'source_file', { config: { schema: { columns: [{ name: 'user_id', type: { kind: 'int64' } }], additional_columns: 'closed' } } }), node('join', 'join', { config: { left_key: 'user_id', right_key: 'user_id' } })]
    const edges = [{ from: 'left', to: 'transform' }, { from: 'transform', to: 'join' }, { from: 'right', to: 'join' }]
    expect(outputSchemaForNode('join', nodes, edges)?.columns.map((column) => column.name)).toEqual(['user_id', 'price', 'status'])
  })
  it('supports the engine aliases and aggregation field alias', () => {
    const result = transformOutputSchema(schema, {
      rules: [
        { type: 'rename', mapping: { status: 'state' } },
        { type: 'drop', columns: ['id'] },
        { type: 'filter', condition: 'state = active' },
        { type: 'aggregate', group_by: ['state'], aggregations: [{ column: 'price', function: 'sum' }] },
      ],
    })
    expect(result?.columns).toEqual([{ name: 'state', type: { kind: 'string' } }, { name: 'sum_price', type: { kind: 'float64' } }])
  })
  it('uses engine aggregate names when aliases are omitted', () => {
    const result = transformOutputSchema(schema, { rules: [{ type: 'aggregate', group_by: ['status'], agg_fields: [{ column: 'price', function: 'min' }, { column: 'id', function: 'count' }] }] })
    expect(result?.columns).toEqual([{ name: 'status', type: { kind: 'string' } }, { name: 'min_price', type: { kind: 'float64' } }, { name: 'count_id', type: { kind: 'int64' } }])
  })
  it('invalidates collisions and malformed aggregate references instead of guessing', () => {
    expect(transformOutputSchema(schema, { rules: [{ type: 'rename_columns', mapping: { id: 'status' } }] })).toBeUndefined()
    expect(transformOutputSchema(schema, { rules: [{ type: 'aggregate', group_by: ['missing'], agg_fields: [] }] })).toBeUndefined()
  })
  it('rejects incompatible join key types and malformed declared schemas', () => {
    expect(joinOutputSchema({ columns: [{ name: 'id', type: { kind: 'int64' } }] }, { columns: [{ name: 'id', type: { kind: 'string' } }] }, { left_key: 'id', right_key: 'id' }).error).toMatch(/incompatible/)
    const nodes = [node('source', 'source_file', { config: { schema: { columns: [{ name: '', type: { kind: 'string' } }] } } })]
    expect(outputSchemaForNode('source', nodes, [])).toBeUndefined()
  })
  it('stops resolving cycles instead of recursing forever', () => {
    const nodes = [node('a', 'transform', { config: { rules: [] } }), node('b', 'transform', { config: { rules: [] } })]
    const edges = [{ from: 'a', to: 'b' }, { from: 'b', to: 'a' }]
    expect(outputSchemaForNode('a', nodes, edges)).toBeUndefined()
  })
  it('passes schemas through partition nodes and compatible unions', () => {
    const schema = { columns: [{ name: 'id', type: { kind: 'int64' } }], additional_columns: 'closed' }
    const nodes = [node('a', 'source_file', { config: { schema } }), node('b', 'source_file', { config: { schema } }), node('map', 'dataset_map'), node('union', 'union')]
    const edges = [{ from: 'a', to: 'map' }, { from: 'map', to: 'union' }, { from: 'b', to: 'union' }]
    expect(outputSchemaForNode('map', nodes, edges)?.columns.map((column) => column.name)).toEqual(['id'])
    expect(outputSchemaForNode('union', nodes, edges)?.columns.map((column) => column.name)).toEqual(['id'])
  })
  it('refuses to guess an incompatible union schema', () => {
    const nodes = [node('a', 'source_file', { config: { schema: { columns: [{ name: 'id', type: { kind: 'int64' } }], additional_columns: 'closed' } } }), node('b', 'source_file', { config: { schema: { columns: [{ name: 'id', type: { kind: 'string' } }], additional_columns: 'closed' } } }), node('union', 'union')]
    expect(outputSchemaForNode('union', nodes, [{ from: 'a', to: 'union' }, { from: 'b', to: 'union' }])).toBeUndefined()
  })
})

describe('click-to-add placement', () => {
  it('uses the anchor when it is free', () => {
    expect(nextFreePosition([], { x: 300, y: 200 })).toEqual({ x: 300, y: 200 })
  })
  it('never stacks a new node on an existing one', () => {
    const anchor = { x: 300, y: 200 }
    const placed: PipelineNode[] = []
    for (let i = 0; i < 3; i++)
      placed.push(node(`n${i}`, 'transform', { position: nextFreePosition(placed, anchor) }))
    for (let a = 0; a < placed.length; a++)
      for (let b = a + 1; b < placed.length; b++) {
        const dx = Math.abs(placed[a].position.x - placed[b].position.x)
        const dy = Math.abs(placed[a].position.y - placed[b].position.y)
        expect(dx >= NODE_WIDTH || dy >= NODE_HEIGHT, `nodes ${a} and ${b} overlap`).toBe(true)
      }
  })
})

describe('auto layout', () => {
  it('places each node right of its furthest predecessor', () => {
    const nodes = [
      node('a', 'source_file'),
      node('b', 'transform'),
      node('c', 'join'),
      node('d', 'source_db'),
    ]
    const edges = [
      { from: 'a', to: 'b' },
      { from: 'b', to: 'c' },
      { from: 'd', to: 'c' },
    ]
    const x = Object.fromEntries(autoLayout(nodes, edges).map((n) => [n.id, n.position.x]))
    expect(x.a).toBe(x.d)
    expect(x.b).toBeGreaterThan(x.a)
    expect(x.c).toBeGreaterThan(x.b)
  })
  it('leaves nodes in a cycle where they are', () => {
    const nodes = [
      node('a', 'transform', { position: { x: 5, y: 7 } }),
      node('b', 'transform', { position: { x: 9, y: 9 } }),
    ]
    const out = autoLayout(nodes, [
      { from: 'a', to: 'b' },
      { from: 'b', to: 'a' },
    ])
    expect(out[0].position).toEqual({ x: 5, y: 7 })
  })
})
