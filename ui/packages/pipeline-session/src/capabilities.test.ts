import { describe, expect, it } from 'vitest'
import { disabledNodeMessage, disabledNodeTypes } from './capabilities'
import { CATALOG, paletteGroups } from './catalog'

const offered = (groups: ReturnType<typeof paletteGroups>) =>
  groups.flatMap((g) => g.items.map((i) => i.type))

describe('disabledNodeTypes', () => {
  it('reads the list the server advertises', () => {
    expect([...disabledNodeTypes({ disabled_node_types: ['bash', 'Code'] })].sort()).toEqual([
      'bash',
      'code',
    ])
  })

  it('treats a server that says nothing, or something malformed, as refusing nothing', () => {
    expect(disabledNodeTypes(undefined).size).toBe(0)
    expect(disabledNodeTypes({}).size).toBe(0)
    expect(disabledNodeTypes({ disabled_node_types: 'bash' as unknown as string[] }).size).toBe(0)
    expect([
      ...disabledNodeTypes({ disabled_node_types: ['bash', 7 as unknown as string] }),
    ]).toEqual(['bash'])
  })

  it('words the refusal as the server does', () => {
    expect(disabledNodeMessage('bash')).toBe(
      'bash nodes are disabled on this deployment (BROKOLI_DISABLED_NODE_TYPES)',
    )
  })
})

describe('paletteGroups', () => {
  it('offers every grouped node type when nothing is disabled', () => {
    const all = CATALOG.filter((e) => e.group).map((e) => e.type)
    expect(offered(paletteGroups('')).sort()).toEqual(all.sort())
    expect(offered(paletteGroups(''))).toContain('bash')
  })

  it('leaves out the types the server refuses, and nothing else', () => {
    const hidden = new Set(['bash', 'code'])
    const types = offered(paletteGroups('', hidden))
    expect(types).not.toContain('bash')
    expect(types).not.toContain('code')
    expect(types.length).toBe(offered(paletteGroups('')).length - 2)
  })

  it('drops a group left empty, and still searches', () => {
    const extensions = paletteGroups('', new Set(['dbt', 'notify', 'bash'])).find(
      (g) => g.group === 'Extensions',
    )
    expect(extensions).toBeUndefined()
    expect(offered(paletteGroups('bash', new Set(['bash'])))).toEqual([])
    expect(offered(paletteGroups('bash'))).toEqual(['bash'])
  })
})
