// #211 — the dashboard cron input's parser must accept exactly what the
// daemon's internal/schedule parser accepts. Both suites run the same
// fixture (internal/schedule/testdata/cron_parity.json; the Go side is
// TestParseMatchesDashboardCronParityFixture).
import { describe, expect, it } from 'vitest';
import { CRON_PRESETS, describeCron, joinCronFields, parseCron, splitCronFields } from '../cron';
import parityRaw from '../../../../internal/schedule/testdata/cron_parity.json?raw';

interface ParityFixture {
  presets: string[];
  cases: { expr: string; valid: boolean; field?: string | null }[];
}

const fixture = JSON.parse(parityRaw) as ParityFixture;

describe('parseCron parity with the server parser', () => {
  it('loads a non-empty shared fixture', () => {
    expect(fixture.cases.length).toBeGreaterThan(20);
  });

  it.each(fixture.cases.map((c) => [JSON.stringify(c.expr), c] as const))(
    'agrees with internal/schedule on %s',
    (_label, c) => {
      const result = parseCron(c.expr);
      expect(result.ok).toBe(c.valid);
      if (!result.ok) expect(result.field).toBe(c.field ?? null);
    },
  );

  it('ships exactly the presets the Go test parses', () => {
    expect(CRON_PRESETS.map((p) => p.expr)).toEqual(fixture.presets);
    for (const p of CRON_PRESETS) expect(parseCron(p.expr).ok).toBe(true);
  });

  it('reports the server error text for a bad field', () => {
    expect(parseCron('* 24 * * *')).toEqual({
      ok: false,
      field: 'hour',
      error: 'hour: value 24 out of range [0,23]',
    });
    expect(parseCron('* * *')).toMatchObject({ ok: false, field: null });
  });

  it('normalises the Quartz form and ? to five Unix fields', () => {
    expect(parseCron('0 0 9 ? * 1-5 *')).toEqual({
      ok: true,
      fields: ['0', '9', '*', '*', '1-5'],
    });
  });
});

describe('splitCronFields / joinCronFields', () => {
  it('round-trips a five-field expression', () => {
    const fields = splitCronFields('0 9 * * 1-5');
    expect(fields).toEqual(['0', '9', '*', '*', '1-5']);
    expect(joinCronFields(fields)).toBe('0 9 * * 1-5');
  });

  it('pads a short expression with empty fields', () => {
    expect(splitCronFields('0 9')).toEqual(['0', '9', '', '', '']);
    expect(splitCronFields('')).toEqual(['', '', '', '', '']);
  });
});

describe('describeCron', () => {
  it.each([
    ['* * * * *', 'Every minute'],
    ['*/5 * * * *', 'Every 5 minutes'],
    ['0 * * * *', 'At minute 0 of every hour'],
    ['0 9 * * *', 'At 09:00 every day'],
    ['0 9 * * 1-5', 'At 09:00 on Monday through Friday'],
    ['0 9 1 * *', 'At 09:00 on day 1 of the month'],
    ['30 18 1,15 * 0', 'At 18:30 on day 1, 15 of the month or on Sunday'],
    ['0 9 * 1-3 *', 'At 09:00 every day in January through March'],
    ['0,30 9-17 * * *', 'At minute 0, 30 past hour 9 through 17'],
    ['*/10 9 * * *', 'Every 10 minutes during hour 9'],
    ['0-30/10 * * * *', 'Every 10 minutes from 0 through 30'],
    ['5/10 * * * *', 'At minute 5 of every hour'],
    ['* * * * */2', 'Every minute on every 2 days'],
  ])('%s reads as "%s"', (expr, text) => {
    expect(describeCron(expr)).toBe(text);
  });

  it('returns null for an invalid expression', () => {
    expect(describeCron('61 * * * *')).toBeNull();
  });
});
