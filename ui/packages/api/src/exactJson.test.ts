import { describe, expect, it } from 'vitest'
import { parseJsonExact } from './exactJson'

describe('parseJsonExact', () => {
  it('keeps integers past 2^53 exact and every other number a number', () => {
    expect(
      parseJsonExact('[{"id":9007199254740993,"max":9223372036854775807,"ratio":1.5}]'),
    ).toEqual([{ id: '9007199254740993', max: '9223372036854775807', ratio: 1.5 }])
  })

  it('leaves safe integers, floats and exponents as numbers', () => {
    expect(
      parseJsonExact(
        '[9007199254740991,-9007199254740991,1234567890123456,1e300,12345678901234567.5,0]',
      ),
    ).toEqual([
      9007199254740991, -9007199254740991, 1234567890123456, 1e300, 12345678901234567.5, 0,
    ])
  })

  it('keeps a 16-digit unsafe integer exact on its own', () => {
    expect(parseJsonExact('{"id":9007199254740993}')).toEqual({ id: '9007199254740993' })
  })

  it('keeps negative unsafe integers exact', () => {
    expect(parseJsonExact('{"min":-9223372036854775808}')).toEqual({ min: '-9223372036854775808' })
  })

  it('does not touch digits inside strings, escaped quotes included', () => {
    const text = '{"note":"id 9007199254740993 and \\"9223372036854775807\\"","n":9007199254740993}'
    expect(parseJsonExact(text)).toEqual({
      note: 'id 9007199254740993 and "9223372036854775807"',
      n: '9007199254740993',
    })
  })

  it('parses like JSON.parse when there is nothing to keep', () => {
    const text = '{"rows":[{"a":1,"b":"x","c":null,"d":[true,2.5]}]}'
    expect(parseJsonExact(text)).toEqual(JSON.parse(text))
  })

  it('still refuses invalid JSON', () => {
    expect(() => parseJsonExact('{"a":9007199254740993,}')).toThrow()
  })
})
