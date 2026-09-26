import { describe, expect, it } from 'vitest'
import { applyPredicateSelection, parseImportedContract } from './ContractGate'

const contract = {
  ir_version: '1.0',
  contract: { id: 'orders', version: '1' },
  input: { kind: 'record-stream' },
  rules: [],
}

describe('contract gate JSON import', () => {
  it('accepts a canonical contract object', () => {
    expect(parseImportedContract(JSON.stringify(contract))).toEqual(contract)
  })

  it('accepts a wrapper containing a canonical contract', () => {
    expect(parseImportedContract(JSON.stringify({ contract }))).toEqual(contract)
  })

  it('rejects arrays and scalars', () => {
    expect(() => parseImportedContract('[]')).toThrow('object')
    expect(() => parseImportedContract('"contract"')).toThrow('object')
  })

  it('rejects malformed top-level contract shapes', () => {
    expect(() => parseImportedContract(JSON.stringify({ ...contract, rules: {} }))).toThrow('rules')
    expect(() =>
      parseImportedContract(JSON.stringify({ ...contract, input: { kind: 'table' } })),
    ).toThrow('record-stream')
  })

  it('couples stream-only predicates to stream rules and count to root path', () => {
    const rule = {
      id: 'r1',
      kind: 'record' as const,
      path: '$.id',
      predicate: { op: 'required' },
      on_breach: { action: 'reject' },
    }
    expect(applyPredicateSelection(rule, 'unique')).toMatchObject({
      kind: 'stream',
      path: '$.id',
      predicate: { op: 'unique' },
    })
    expect(applyPredicateSelection(rule, 'count')).toMatchObject({
      kind: 'stream',
      path: '$',
      predicate: { op: 'count' },
    })
  })
})
