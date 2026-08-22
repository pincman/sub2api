import { describe, expect, it } from 'vitest'
import { isUpdateCapableBuild } from '../version'

describe('isUpdateCapableBuild', () => {
  it('allows official and custom release builds', () => {
    expect(isUpdateCapableBuild('release')).toBe(true)
    expect(isUpdateCapableBuild('custom')).toBe(true)
  })

  it('keeps source builds manual-only', () => {
    expect(isUpdateCapableBuild('source')).toBe(false)
    expect(isUpdateCapableBuild('')).toBe(false)
    expect(isUpdateCapableBuild(null)).toBe(false)
  })
})
