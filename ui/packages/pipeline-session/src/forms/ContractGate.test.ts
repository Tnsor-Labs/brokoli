import { describe, expect, it } from 'vitest'
import { parseImportedContract } from './ContractGate'

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
})
