// The command the popup offers when the daemon is unreachable. It launches
// the background helper (detaches, pid file, logs under the state dir) rather
// than the foreground watcher, so the user's terminal is not held.

export interface NavigatorLike {
  userAgent: string
  userAgentData?: { platform?: string }
}

export function isWindowsPlatform(nav: NavigatorLike): boolean {
  const platform = nav.userAgentData?.platform
  if (platform) return /windows/i.test(platform)
  return /windows/i.test(nav.userAgent)
}

export function buildStartCommand(installDir: string | null, isWindows: boolean): string {
  if (isWindows) {
    const script = '.\\dualsub-bg.ps1 start'
    if (!installDir) return script
    // PowerShell single-quoted strings escape a quote by doubling it.
    const quoted = installDir.replaceAll("'", "''")
    return `cd '${quoted}'; ${script}`
  }
  const script = './dualsub-bg.sh start'
  if (!installDir) return script
  // POSIX shell: close the quote, add an escaped quote, reopen.
  const quoted = installDir.replaceAll("'", "'\\''")
  return `cd '${quoted}' && ${script}`
}
