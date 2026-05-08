import type { TranscriptEntry } from '@/shared/transcript'

function parseTimestamp(ts: string): number {
  const parts = ts.replace(',', '.').split(':').map(Number)
  if (parts.length === 3) return parts[0] * 3600 + parts[1] * 60 + parts[2]
  if (parts.length === 2) return parts[0] * 60 + parts[1]
  return parts[0]
}

const TIMING = /(\d+:\d+(?::\d+)?[\.,]\d+)\s+-->\s+(\d+:\d+(?::\d+)?[\.,]\d+)/

export function parseVtt(vttText: string): TranscriptEntry[] {
  const entries: TranscriptEntry[] = []
  const blocks = vttText.split(/\n\s*\n/)
  let index = 1

  for (const block of blocks) {
    const lines = block.trim().split('\n')
    const timingIdx = lines.findIndex((l) => l.includes('-->'))
    if (timingIdx === -1) continue

    const timingMatch = lines[timingIdx].match(TIMING)
    if (!timingMatch) continue

    const startTime = parseTimestamp(timingMatch[1])
    const endTime = parseTimestamp(timingMatch[2])

    const text = lines
      .slice(timingIdx + 1)
      .join(' ')
      .replace(/<[^>]+>/g, '')
      .trim()

    if (!text) continue
    entries.push({ index: index++, originalText: text, startTime, endTime })
  }

  return entries
}
