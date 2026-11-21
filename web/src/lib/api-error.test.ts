import { describe, expect, it } from 'vitest'
import { apiErrorCode, apiErrorMessage } from './api-error'

describe('apiErrorMessage', () => {
  it('reads the envelope', () => {
    const body = { success: false, error: { code: 'unauthorized', message: 'Invalid email or password' }, message: 'Invalid email or password' }
    expect(apiErrorMessage(body)).toBe('Invalid email or password')
    expect(apiErrorMessage({ success: false, error: { code: 'conflict', message: 'Busy' } })).toBe('Busy')
  })

  it('reads the shapes panels sent before the envelope', () => {
    expect(apiErrorMessage({ error: 'IP address not whitelisted' })).toBe('IP address not whitelisted')
    expect(apiErrorMessage({ error: 'Forbidden', message: 'Insufficient permissions' })).toBe('Insufficient permissions')
  })

  it('never returns an object, and falls back when there are no words', () => {
    expect(apiErrorMessage({ error: { code: 'x' } }, 'Try again')).toBe('Try again')
    expect(apiErrorMessage(null, 'Try again')).toBe('Try again')
    expect(apiErrorMessage('oops', 'Try again')).toBe('Try again')
    expect(apiErrorMessage({})).toBe('')
  })
})

describe('apiErrorCode', () => {
  it('prefers a top-level code and else reads error.code', () => {
    expect(apiErrorCode({ code: 'maintenance', error: { code: 'unavailable' } })).toBe('maintenance')
    expect(apiErrorCode({ error: { code: 'two_factor_required', message: 'Set up two-factor' } })).toBe('two_factor_required')
    expect(apiErrorCode({ error: 'text only' })).toBe('')
    expect(apiErrorCode(undefined)).toBe('')
  })
})
