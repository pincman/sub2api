import { describe, expect, it } from 'vitest'

import { formatDisplayVersion, isUpdateCapableBuild } from '../version'

describe('version utils', () => {
  it('formats custom build versions for display without the commit suffix', () => {
    expect(formatDisplayVersion('0.1.179-custom.9a4d18309c7a')).toBe('0.1.179-custom')
    expect(formatDisplayVersion('v0.1.179-custom.abc123')).toBe('v0.1.179-custom')
  })

  it('keeps non-custom versions unchanged', () => {
    expect(formatDisplayVersion('v0.1.179')).toBe('v0.1.179')
  })

  it('returns an empty string for blank values', () => {
    expect(formatDisplayVersion('   ')).toBe('')
    expect(formatDisplayVersion(null)).toBe('')
  })

  it('keeps update-capable build detection unchanged', () => {
    expect(isUpdateCapableBuild('release')).toBe(true)
    expect(isUpdateCapableBuild('custom')).toBe(true)
    expect(isUpdateCapableBuild('source')).toBe(false)
    expect(isUpdateCapableBuild('')).toBe(false)
    expect(isUpdateCapableBuild(null)).toBe(false)
  })
})
