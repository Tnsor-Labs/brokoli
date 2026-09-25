import { describe, expect, it } from 'vitest'
import { schemaErrorDetails } from './NodeData'

describe('schemaErrorDetails', () => {
  it('extracts a missing column and available columns', () => {
    expect(schemaErrorDetails(`column "email" not found in available columns: [id, name]`)).toEqual(
      { missing: 'email', available: ['id', 'name'] },
    )
  })
  it('handles join-key errors without inventing available columns', () => {
    expect(schemaErrorDetails(`join key 'user_id' not found in right dataset`)).toEqual({
      missing: 'user_id',
      available: undefined,
    })
  })
  it('does not relabel unrelated errors as schema errors', () => {
    expect(schemaErrorDetails('connection timed out')).toBeUndefined()
  })
})
