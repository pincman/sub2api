/** Whether a build can download and apply a published release in place. */
export function isUpdateCapableBuild(buildType?: string | null): boolean {
  return buildType === 'release' || buildType === 'custom'
}
