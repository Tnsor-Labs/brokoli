import { describe, expect, it } from 'vitest'
import { passwordFields } from './passwordSource'
import { sameSettings } from '../secret-stores/SecretStoresPage'

const ref = { store: 'vault-prod', path: 'warehouse', version: '', field: 'password' }
const empty = { store: '', path: '', version: '', field: '' }

describe('password save body', () => {
  it('sends the composed reference, and no password, from a secret store', () => {
    expect(passwordFields('store', ref, 'typed', 'encrypted://********', false)).toEqual({
      password_ref: 'secret://vault-prod/warehouse#password',
    })
  })

  it('keeps a stored password when nothing is typed, by echoing its mask', () => {
    expect(passwordFields('stored', empty, '', 'encrypted://********', false)).toEqual({
      password_ref: 'encrypted://********',
    })
  })

  it('replaces a secret-store reference with a typed password', () => {
    expect(
      passwordFields('stored', empty, 'new-pw', 'secret://vault-prod/warehouse#password', false),
    ).toEqual({ password: 'new-pw' })
  })

  it('keeps an operator reference and ignores a typed password', () => {
    expect(passwordFields('stored', empty, 'ignored', 'env://WAREHOUSE_PASSWORD', true)).toEqual({
      password_ref: 'env://WAREHOUSE_PASSWORD',
    })
  })

  it('sends a new password on create', () => {
    expect(passwordFields('stored', empty, 'pw', undefined, false)).toEqual({ password: 'pw' })
  })
})

describe('store settings comparison', () => {
  it('ignores key order, as the server returns its own', () => {
    expect(sameSettings({ address: 'a', mount: 'm' }, { mount: 'm', address: 'a' })).toBe(true)
    expect(sameSettings({ address: 'a' }, { address: 'b' })).toBe(false)
    expect(sameSettings({ address: 'a' }, { address: 'a', mount: 'm' })).toBe(false)
  })
})
