/** Whether a build can download and apply a published release in place. */
export function isUpdateCapableBuild(buildType?: string | null): boolean {
  return buildType === 'release' || buildType === 'custom'
}

/**
 * Format a version string for UI display.
 *
 * Custom builds are published as `...-custom.<hash>` but the sidebar only
 * needs to show the stable `...-custom` label.
 */
export function formatDisplayVersion(version?: string | null): string {
  const value = String(version ?? '').trim()
  if (!value) return ''

  return value.replace(/(-custom)\.[0-9a-f]+$/i, '$1')
}
