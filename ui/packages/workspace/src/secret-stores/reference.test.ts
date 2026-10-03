import { describe, expect, it } from 'vitest'
import {
  composeSecretRef,
  isSecretRef,
  parseSecretRef,
  secretRefProblem,
  setExtraReference,
} from './reference'

describe('secret references', () => {
  it('round-trips through parse and compose', () => {
    for (const ref of [
      'secret://vault-prod/warehouse/loader#password',
      'secret://aws/prod/warehouse?version=3#pw',
      'secret://ssm//prod/warehouse/password',
      'secret://kv/arn:aws:secretsmanager:eu-west-1:1:secret:x',
      'secret://aws/prod/warehouse?version=AWSPREVIOUS',
    ]) {
      const parts = parseSecretRef(ref)
      expect(parts, ref).not.toBeNull()
      expect(composeSecretRef(parts!)).toBe(ref)
    }
  })

  it('keeps a leading slash in the path, as an SSM parameter needs', () => {
    expect(parseSecretRef('secret://ssm//prod/api-token')).toEqual({
      store: 'ssm',
      path: '/prod/api-token',
      version: '',
      field: '',
    })
  })

  it('refuses what the server refuses', () => {
    for (const bad of [
      'vault://x#y',
      'secret://vault',
      'secret://Vault/x',
      'secret://-a/x',
      'secret://v/a/../b',
      'secret://v/x#',
      'secret://v/x?ttl=1',
      'secret://v/',
    ]) {
      expect(parseSecretRef(bad), bad).toBeNull()
    }
    expect(isSecretRef('env://X')).toBe(false)
    expect(isSecretRef(undefined)).toBe(false)
  })

  it('requires a field for maps and refuses one for single values', () => {
    const parts = { store: 'v', path: 'p', version: '', field: '' }
    expect(secretRefProblem(parts, 'map')).toMatch(/name one/)
    expect(secretRefProblem({ ...parts, field: 'f' }, 'string')).toMatch(/leave the field empty/)
    expect(secretRefProblem(parts, 'string')).toBe('')
    expect(secretRefProblem({ ...parts, field: 'f' }, 'either')).toBe('')
    expect(secretRefProblem({ ...parts, store: '' }, 'map')).toMatch(/store/)
    expect(secretRefProblem({ ...parts, path: '/' }, 'string')).toMatch(/path/)
  })

  it('sets one extra value to a reference and keeps the rest', () => {
    const out = JSON.parse(
      setExtraReference(
        '{"bucket":"exports","access_key":"AK"}',
        'secret_key',
        'secret://aws/s3#secret_key',
      ),
    )
    expect(out).toEqual({
      bucket: 'exports',
      access_key: 'AK',
      secret_key: 'secret://aws/s3#secret_key',
    })
    expect(JSON.parse(setExtraReference('', 'key', 'secret://k/v'))).toEqual({
      key: 'secret://k/v',
    })
    expect(JSON.parse(setExtraReference('not json', 'key', 'secret://k/v'))).toEqual({
      key: 'secret://k/v',
    })
  })
})
