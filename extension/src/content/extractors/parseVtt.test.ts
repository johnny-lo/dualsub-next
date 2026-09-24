import { describe, expect, it } from 'vitest'
import { parseVtt } from './parseVtt'

describe('parseVtt', () => {
  it('joins multi-line cues with a single space for CRLF files', () => {
    const vtt =
      'WEBVTT\r\n\r\n1\r\n00:00:01.000 --> 00:00:02.000\r\nHello\r\nworld\r\n\r\n' +
      '2\r\n00:00:03.000 --> 00:00:04.000\r\nSecond cue\r\n'
    const entries = parseVtt(vtt)
    expect(entries.map((e) => e.originalText)).toEqual(['Hello world', 'Second cue'])
  })
})
