export interface TranscriptEntry {
  index: number
  originalText: string
  startTime?: number
  endTime?: number
}

export interface TranscriptPayload {
  site: 'netflix' | 'udemy'
  videoKey: string
  title: string
  sourceLang?: string
  entries: TranscriptEntry[]
}

// Player DOM textContent diverges from VTT source text (smart quotes,
// half/full-width punctuation, stripped inline tags, line-break differences).
// Normalize aggressively so VTT-keyed translations still match cue text the
// player renders. Keeps CJK characters via Unicode property escapes — \w would
// drop them.
export function normalizeText(text: string): string {
  return text
    .normalize('NFKC')
    .toLowerCase()
    .replace(/[‘’‚‛′]/g, "'")
    .replace(/[“”„‟″]/g, '"')
    .replace(/[–—−]/g, '-')
    .replace(/[…]/g, '...')
    .replace(/[\p{P}\p{S}]/gu, ' ')
    .replace(/\s+/g, ' ')
    .trim()
}

export function formatTranscriptForExport(
  entries: TranscriptEntry[],
  targetLang = '繁體中文',
): string {
  const header = `請將以下字幕翻譯成${targetLang}，保持 [序號] 格式，每行一句：\n\n`
  const body = entries.map((e) => `[${e.index}] ${e.originalText}`).join('\n')
  return header + body
}

export function parseTranslatedTranscript(
  text: string,
): Array<{ index: number; translatedText: string }> {
  const results: Array<{ index: number; translatedText: string }> = []
  const regex = /^\[(\d+)\]\s*(.+)$/
  for (const line of text.split('\n')) {
    const match = line.trim().match(regex)
    if (match) {
      results.push({
        index: parseInt(match[1], 10),
        translatedText: match[2].trim(),
      })
    }
  }
  return results
}
