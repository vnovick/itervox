// DeepPartial<T> + applyOverrides: a small deep-merge that lets fixture
// factories accept terse overrides like `makeSnapshot({ counts: { running: 2 } })`
// without specifying every required field.
//
// Semantics:
//   - Plain objects are merged recursively.
//   - Arrays are replaced wholesale (matches Vitest test ergonomics —
//     overriding `running: [...]` always means "use these rows, not merged rows").
//   - `null` / `undefined` overrides REPLACE the base value (so callers can
//     null-out an optional field).
//   - Dates and class instances are replaced wholesale (typeof check).

export type DeepPartial<T> = T extends Date
  ? T
  : T extends Array<infer U>
    ? Array<DeepPartial<U>>
    : T extends object
      ? { [K in keyof T]?: DeepPartial<T[K]> }
      : T;

function isPlainObject(v: unknown): v is Record<string, unknown> {
  if (v === null || typeof v !== 'object') return false;
  if (Array.isArray(v)) return false;
  if (v instanceof Date) return false;
  const proto = Object.getPrototypeOf(v);
  return proto === Object.prototype || proto === null;
}

export function applyOverrides<T>(base: T, overrides?: DeepPartial<T>): T {
  if (overrides === undefined) return base;
  if (!isPlainObject(base) || !isPlainObject(overrides)) {
    return overrides as T;
  }
  const out: Record<string, unknown> = { ...(base as Record<string, unknown>) };
  for (const [k, v] of Object.entries(overrides as Record<string, unknown>)) {
    if (v === undefined) {
      out[k] = undefined;
    } else if (isPlainObject(v) && isPlainObject(out[k])) {
      out[k] = applyOverrides(out[k], v as DeepPartial<unknown>);
    } else {
      out[k] = v;
    }
  }
  return out as T;
}
