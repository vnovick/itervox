// #211 — client-side mirror of the daemon's cron parser
// (internal/schedule/cron.go), used by the automation editor's light cron
// input for validation and a human-readable preview. It accepts exactly what
// `schedule.Parse` accepts — no more (no @aliases, no month/day names, no `L`
// or `#`) — so an expression the editor calls valid is one the daemon will
// schedule. Parity is pinned by a shared fixture
// (internal/schedule/testdata/cron_parity.json) that both the Go test and the
// Vitest suite run.

export type CronFieldName = 'minute' | 'hour' | 'day-of-month' | 'month' | 'day-of-week';

export interface CronFieldSpec {
  name: CronFieldName;
  label: string;
  min: number;
  max: number;
}

/** The five fields in order, with the same bounds as the Go parser. */
export const CRON_FIELDS: readonly CronFieldSpec[] = [
  { name: 'minute', label: 'Minute', min: 0, max: 59 },
  { name: 'hour', label: 'Hour', min: 0, max: 23 },
  { name: 'day-of-month', label: 'Day', min: 1, max: 31 },
  { name: 'month', label: 'Month', min: 1, max: 12 },
  { name: 'day-of-week', label: 'Weekday', min: 0, max: 6 },
];

export type CronFields = [string, string, string, string, string];

export type CronParseResult =
  | { ok: true; fields: CronFields }
  | { ok: false; field: CronFieldName | null; error: string };

export interface CronPreset {
  id: string;
  label: string;
  expr: string;
}

export const CRON_PRESETS: readonly CronPreset[] = [
  { id: 'every-minute', label: 'Every minute', expr: '* * * * *' },
  { id: 'every-5-minutes', label: 'Every 5 minutes', expr: '*/5 * * * *' },
  { id: 'every-15-minutes', label: 'Every 15 minutes', expr: '*/15 * * * *' },
  { id: 'hourly', label: 'Every hour', expr: '0 * * * *' },
  { id: 'daily', label: 'Every day at 09:00', expr: '0 9 * * *' },
  { id: 'weekdays', label: 'Weekdays at 09:00', expr: '0 9 * * 1-5' },
  { id: 'weekly', label: 'Mondays at 09:00', expr: '0 9 * * 1' },
  { id: 'monthly', label: 'First of the month at 09:00', expr: '0 9 1 * *' },
];

const INT64_MAX = 9223372036854775807n;
const INT64_MIN = -9223372036854775808n;

/** Go's strconv.Atoi on a 64-bit platform: optional sign, ASCII digits, int64 range. */
function atoi(text: string): number | null {
  if (!/^[+-]?[0-9]+$/.test(text)) return null;
  const big = BigInt(text);
  if (big > INT64_MAX || big < INT64_MIN) return null;
  // Beyond 2^53 the value only ever feeds a range check or a positive step,
  // so the precision loss of Number() cannot flip a result.
  return Number(big);
}

function parseBound(text: string, min: number, max: number): number {
  const value = atoi(text);
  if (value === null) throw new Error(`invalid integer "${text}"`);
  if (value < min || value > max) {
    throw new Error(`value ${String(value)} out of range [${String(min)},${String(max)}]`);
  }
  return value;
}

function parseRange(text: string, min: number, max: number): [number, number] {
  if (text === '' || text === '*') return [min, max];
  if (text.includes('-')) {
    const pieces = text.split('-');
    if (pieces.length !== 2) throw new Error(`invalid range "${text}"`);
    const start = parseBound(pieces[0], min, max);
    const end = parseBound(pieces[1], min, max);
    if (end < start) throw new Error(`descending range "${text}"`);
    return [start, end];
  }
  const value = parseBound(text, min, max);
  return [value, value];
}

function checkPart(part: string, min: number, max: number): void {
  if (part === '') throw new Error('empty field part');
  let base = part;
  if (part.includes('/')) {
    const pieces = part.split('/');
    if (pieces.length !== 2) throw new Error(`invalid step expression "${part}"`);
    base = pieces[0];
    const step = atoi(pieces[1]);
    if (step === null || step <= 0) throw new Error(`invalid step value "${pieces[1]}"`);
  }
  parseRange(base, min, max);
}

/** Validates one field's text against its bounds; throws with the Go message. */
function checkField(text: string, spec: CronFieldSpec): void {
  if (text === '*') return;
  for (const part of text.split(',')) checkPart(part.trim(), spec.min, spec.max);
}

/**
 * Parses an expression with the same rules as `schedule.Parse`: five Unix
 * fields, or the 6/7-field Quartz form whose seconds field is exactly `0`
 * and whose year field (7 fields) is exactly `*`; a whole-field `?` reads as
 * `*`. On success `fields` is the normalised five-field form.
 */
export function parseCron(expr: string): CronParseResult {
  const trimmed = expr.trim();
  let tokens = trimmed === '' ? [] : trimmed.split(/\s+/);
  if (tokens.length === 6 || tokens.length === 7) {
    if (tokens[0] !== '0') {
      return {
        ok: false,
        field: null,
        error: `quartz seconds field must be 0 (minute is the smallest schedulable unit), got "${tokens[0]}"`,
      };
    }
    if (tokens.length === 7 && tokens[6] !== '*') {
      return {
        ok: false,
        field: null,
        error: `quartz year field must be * (year-pinned schedules are not supported), got "${tokens[6]}"`,
      };
    }
    tokens = tokens.slice(1, 6);
  } else if (tokens.length !== 5) {
    return {
      ok: false,
      field: null,
      error: `expected 5 cron fields (or 6/7 Quartz fields), got ${String(tokens.length)}`,
    };
  }
  const fields = tokens.map((t) => (t === '?' ? '*' : t)) as CronFields;
  for (let i = 0; i < CRON_FIELDS.length; i += 1) {
    const spec = CRON_FIELDS[i];
    try {
      checkField(fields[i], spec);
    } catch (err) {
      return { ok: false, field: spec.name, error: `${spec.name}: ${(err as Error).message}` };
    }
  }
  return { ok: true, fields };
}

/** Splits an expression into five editable fields (normalising the Quartz form when valid). */
export function splitCronFields(expr: string): CronFields {
  const parsed = parseCron(expr);
  if (parsed.ok) return parsed.fields;
  const tokens = expr.trim() === '' ? [] : expr.trim().split(/\s+/);
  return [0, 1, 2, 3, 4].map((i) => tokens[i] ?? '') as CronFields;
}

export function joinCronFields(fields: readonly string[]): string {
  return fields.map((f) => f.trim()).join(' ');
}

const MONTHS = [
  'January',
  'February',
  'March',
  'April',
  'May',
  'June',
  'July',
  'August',
  'September',
  'October',
  'November',
  'December',
];
const WEEKDAYS = ['Sunday', 'Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday'];

type Namer = (n: number) => string;
const plain: Namer = (n) => String(n);
const monthName: Namer = (n) => MONTHS[n - 1] ?? String(n);
const weekdayName: Namer = (n) => WEEKDAYS[n] ?? String(n);

function describePart(part: string, unit: string, name: Namer): string {
  const range = (text: string) => {
    if (text.includes('-')) {
      const [a, b] = text.split('-');
      return `${name(Number(a))} through ${name(Number(b))}`;
    }
    return name(Number(text));
  };
  const slash = part.indexOf('/');
  if (slash === -1) return range(part);
  const base = part.slice(0, slash);
  const step = Number(part.slice(slash + 1));
  const every = step === 1 ? `every ${unit}` : `every ${String(step)} ${unit}s`;
  if (base === '' || base === '*') return every;
  if (base.includes('-')) return `${every} from ${range(base)}`;
  // `5/10` steps from 5 to the end of the range 5-5: just 5 (as in Go).
  return range(base);
}

function describeList(field: string, unit: string, name: Namer): string {
  return field
    .split(',')
    .map((p) => describePart(p, unit, name))
    .join(', ');
}

const isSingle = (f: string) => /^[+]?[0-9]+$/.test(f);
const pad = (n: number) => String(n).padStart(2, '0');

function describeTime(minute: string, hour: string): string {
  if (isSingle(minute) && isSingle(hour)) {
    return `At ${pad(Number(hour))}:${pad(Number(minute))}`;
  }
  // One stepped part over a range (`*/5`, `/5`, `0-30/10`) repeats; a
  // stepped single value (`5/10`) is just that value, as in Go.
  const stepped = !minute.includes(',') && minute.includes('/') && !isSingle(minute.split('/')[0]);
  const repeating = minute === '*' || stepped;
  const minutes =
    minute === '*'
      ? 'Every minute'
      : stepped
        ? describePart(minute, 'minute', plain).replace(/^every/, 'Every')
        : `At minute ${describeList(minute, 'minute', plain)}`;
  if (hour === '*') return repeating ? minutes : `${minutes} of every hour`;
  return `${minutes} ${repeating ? 'during' : 'past'} hour ${describeList(hour, 'hour', plain)}`;
}

/**
 * A plain-English reading of a valid expression, e.g. `0 9 * * 1-5` →
 * "At 09:00 on Monday through Friday". Day-of-month and day-of-week join
 * with "or" when both are set, matching the daemon's cron OR semantics.
 */
export function describeCron(expr: string): string | null {
  const parsed = parseCron(expr);
  if (!parsed.ok) return null;
  const [minute, hour, dom, month, dow] = parsed.fields;
  const time = describeTime(minute, hour);
  const days: string[] = [];
  if (dom !== '*') days.push(`on day ${describeList(dom, 'day', plain)} of the month`);
  if (dow !== '*') days.push(`on ${describeList(dow, 'day', weekdayName)}`);
  let text = time;
  if (days.length > 0) text += ` ${days.join(' or ')}`;
  else if (isSingle(minute) && isSingle(hour)) text += ' every day';
  if (month !== '*') text += ` in ${describeList(month, 'month', monthName)}`;
  return text;
}
