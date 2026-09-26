import { describe, expect, it } from 'vitest'
import { applyPredicateSelection, kindForPredicate, parseImportedContract } from './ContractGate'

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

  // The direction the coupling used to miss. Choosing Stream and then a
  // record predicate left kind "stream", which the contract validator
  // refuses with "required must be a record rule". The kind is now
  // derived, so there is no state the form can be left in that the
  // validator would reject on this basis.
  it('derives a record kind back from a stream rule', () => {
    const streamRule = {
      id: 'r1',
      kind: 'stream' as const,
      path: '$.email',
      predicate: { op: 'unique' },
      on_breach: { action: 'reject' },
    }
    expect(applyPredicateSelection(streamRule, 'required')).toMatchObject({
      kind: 'record',
      predicate: { op: 'required' },
    })
  })

  // Every operator the form offers has exactly one kind the validator
  // accepts. If an operator is added without one, this fails rather
  // than shipping a choice that cannot work.
  it('maps every offered predicate to the kind the validator demands', () => {
    const expected: Record<string, 'record' | 'stream'> = {
      required: 'record',
      not_null: 'record',
      type: 'record',
      format: 'record',
      regex: 'record',
      range: 'record',
      enum: 'record',
      unique: 'stream',
      count: 'stream',
    }
    for (const [op, kind] of Object.entries(expected)) {
      expect(kindForPredicate(op)).toBe(kind)
    }
  })

  // An imported contract may use a predicate this UI does not list.
  // Rewriting its kind on a guess would corrupt a valid contract.
  it('leaves an unknown predicate as a record rule', () => {
    expect(kindForPredicate('some_future_op')).toBe('record')
  })
})
