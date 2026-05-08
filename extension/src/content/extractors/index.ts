import { NetflixExtractor } from './NetflixExtractor'
import { UdemyExtractor } from './UdemyExtractor'
import type { SubtitleExtractor } from './types'

export function detectSite(): SubtitleExtractor | null {
  const host = location.hostname
  if (host.includes('netflix.com')) return new NetflixExtractor()
  if (host.includes('udemy.com')) return new UdemyExtractor()
  return null
}

export type { SubtitleExtractor } from './types'
