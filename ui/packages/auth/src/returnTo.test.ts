// @vitest-environment jsdom
import { describe, expect, it } from 'vitest'
import { isSafeReturn, rememberReturn, takeReturn } from './returnTo'

describe('isSafeReturn', () => {
  it('accepts in-app routes with their query', () => {
    expect(isSafeReturn('/device?code=QJCS-MGTG')).toBe(true)
    expect(isSafeReturn('/invite/abc')).toBe(true)
  })
  it('refuses anything that could leave the application or loop back to sign-in', () => {
    for (const bad of ['//evil.example', 'https://evil.example', '/x?next=https://evil', '\\\\evil', '/\\evil', 'device', '/login', '/login?x=1', '/auth-callback?token=t', '', null, undefined]) {
      expect(isSafeReturn(bad)).toBe(false)
    }
  })
})

describe('remember and take', () => {
  it('returns the route once', () => {
    rememberReturn('/device?code=QJCS-MGTG')
    expect(takeReturn()).toBe('/device?code=QJCS-MGTG')
    expect(takeReturn()).toBeNull()
  })
  it('clears the memory for an unsafe route', () => {
    rememberReturn('/pipelines')
    rememberReturn('//evil.example')
    expect(takeReturn()).toBeNull()
  })
})
