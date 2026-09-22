import { describe, expect, it } from 'vitest'
import { buildStartCommand, isWindowsPlatform } from './startCommand'

describe('buildStartCommand', () => {
  it('starts the daemon in the background on Linux/macOS', () => {
    expect(buildStartCommand('/home/j/dualsub', false)).toBe(
      "cd '/home/j/dualsub' && ./dualsub-bg.sh start",
    )
  })

  it('starts the daemon in the background on Windows', () => {
    expect(buildStartCommand('C:\\Users\\j\\dualsub', true)).toBe(
      "cd 'C:\\Users\\j\\dualsub'; .\\dualsub-bg.ps1 start",
    )
  })

  it('omits the cd when the install dir is unknown', () => {
    expect(buildStartCommand(null, false)).toBe('./dualsub-bg.sh start')
    expect(buildStartCommand(null, true)).toBe('.\\dualsub-bg.ps1 start')
  })

  it('escapes single quotes per shell', () => {
    expect(buildStartCommand("/tmp/it's", false)).toBe("cd '/tmp/it'\\''s' && ./dualsub-bg.sh start")
    expect(buildStartCommand("C:\\it's", true)).toBe("cd 'C:\\it''s'; .\\dualsub-bg.ps1 start")
  })
})

describe('isWindowsPlatform', () => {
  it('prefers userAgentData.platform', () => {
    expect(isWindowsPlatform({ userAgent: 'X11; Linux', userAgentData: { platform: 'Windows' } })).toBe(true)
    expect(isWindowsPlatform({ userAgent: 'Windows NT', userAgentData: { platform: 'Linux' } })).toBe(false)
  })

  it('falls back to the user agent string', () => {
    expect(isWindowsPlatform({ userAgent: 'Mozilla/5.0 (Windows NT 10.0)' })).toBe(true)
    expect(isWindowsPlatform({ userAgent: 'Mozilla/5.0 (X11; Linux x86_64)' })).toBe(false)
  })
})
