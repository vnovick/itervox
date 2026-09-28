import { describe, it, expect } from 'vitest';
import {
  LIVE_LOG_ENTRY_CAP,
  applyLogEntry,
  applyLogGap,
  emptyLogStream,
  parseStreamId,
} from '../logStream';
import type { IssueLogEntry } from '../../types/schemas';

const line = (message: string): IssueLogEntry => ({ level: 'INFO', event: 'text', message });

describe('parseStreamId', () => {
  it('parses epoch-seq and bare seq ids', () => {
    expect(parseStreamId('42-7')).toEqual({ generation: '42', seq: 7 });
    expect(parseStreamId('7')).toEqual({ generation: '', seq: 7 });
  });

  it('rejects ids without a numeric seq', () => {
    expect(parseStreamId(undefined)).toBeNull();
    expect(parseStreamId('')).toBeNull();
    expect(parseStreamId('42-')).toBeNull();
    expect(parseStreamId('abc')).toBeNull();
  });
});

describe('applyLogGap', () => {
  it('records the position without a marker when nothing is shown yet', () => {
    const s = applyLogGap(emptyLogStream('ENG-1'), 'ENG-1', 0, '7-10');
    expect(s.entries).toEqual([]);
    expect(s.lastSeq).toBe(10);
    expect(s.generation).toBe('7');
  });

  it('still shows a marker when the gap does not advance the numbering', () => {
    let s = applyLogEntry(emptyLogStream('ENG-1'), 'ENG-1', 0, '7-3', line('three'));
    s = applyLogGap(s, 'ENG-1', 0, '7-3');
    expect(s.entries.map((e) => e.message)).toEqual([
      'three',
      expect.stringMatching(/^Some log lines were skipped/) as unknown as string,
    ]);
  });

  it('replaces the list when the gap comes from a new generation, even past lastSeq', () => {
    let s = applyLogEntry(emptyLogStream('ENG-1'), 'ENG-1', 0, '7-1', line('old-1'));
    s = applyLogEntry(s, 'ENG-1', 0, '7-2', line('old-2'));
    s = applyLogGap(s, 'ENG-1', 0, '9-5');
    expect(s.entries.map((e) => e.message)).toEqual([
      expect.stringMatching(/reset/) as unknown as string,
    ]);
    expect(s.generation).toBe('9');
    expect(s.lastSeq).toBe(5);
  });

  it('replaces the list when a line from a new generation arrives past lastSeq without a gap', () => {
    let s = applyLogEntry(emptyLogStream('ENG-1'), 'ENG-1', 0, '7-1', line('old-1'));
    s = applyLogEntry(s, 'ENG-1', 0, '9-5', line('new'));
    expect(s.entries.map((e) => e.message)).toEqual([
      expect.stringMatching(/reset/) as unknown as string,
      'new',
    ]);
  });

  it('belongs to the stream instance it was applied for', () => {
    const s = applyLogEntry(emptyLogStream('ENG-1', 1), 'ENG-1', 1, '7-3', line('three'));
    const next = applyLogGap(s, 'ENG-1', 2, '7-9');
    expect(next.instance).toBe(2);
    expect(next.entries).toEqual([]);
  });
});

describe('applyLogEntry', () => {
  it('replaces the list with a reset marker when a line arrives from a new generation', () => {
    let s = applyLogEntry(emptyLogStream('ENG-1'), 'ENG-1', 0, '7-1', line('old'));
    s = applyLogEntry(s, 'ENG-1', 0, '9-1', line('new'));
    expect(s.entries.map((e) => e.message)).toEqual([expect.stringMatching(/reset/), 'new']);
    expect(s.generation).toBe('9');
  });

  it('keeps at most LIVE_LOG_ENTRY_CAP lines', () => {
    let s = emptyLogStream('ENG-1');
    for (let i = 1; i <= LIVE_LOG_ENTRY_CAP + 3; i += 1) {
      s = applyLogEntry(s, 'ENG-1', 0, `7-${String(i)}`, line(String(i)));
    }
    expect(s.entries).toHaveLength(LIVE_LOG_ENTRY_CAP);
    expect(s.entries[0].message).toBe('4');
    expect(s.lastSeq).toBe(LIVE_LOG_ENTRY_CAP + 3);
  });
});
