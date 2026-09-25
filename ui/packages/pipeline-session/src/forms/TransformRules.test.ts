import { describe, expect, it } from 'vitest'
import type { DatasetSchema } from '../document'
import { expressionType } from './TransformRules'

const schema: DatasetSchema = {
  contract: 'brokoli.dataset-schema/v1',
  columns: [
    { name: 'quantity', type: { kind: 'int64' } },
    { name: 'price', type: { kind: 'decimal', precision: 12, scale: 2 } },
    { name: 'label', type: { kind: 'string' } },
  ],
  additional_columns: 'closed',
}

describe('transform expression type preview', () => {
  it('uses the declared type for a direct column reference', () => {
    expect(expressionType('label', schema)).toEqual({ kind: 'string' })
  })
  it('infers numeric and boolean literals', () => {
    expect(expressionType('42', schema)).toEqual({ kind: 'int64' })
    expect(expressionType('true', schema)).toEqual({ kind: 'boolean' })
  })
  it('preserves decimal metadata for decimal arithmetic', () => {
    expect(expressionType('price * price', schema)).toEqual({
      kind: 'decimal',
      precision: 12,
      scale: 2,
    })
  })
  it('uses float output for division', () => {
    expect(expressionType('quantity / 2', schema)).toEqual({ kind: 'float64' })
  })
})
