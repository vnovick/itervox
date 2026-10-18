// Rules editor JSON view (#157): JSONC text → validated rules.
//
// The daemon's files are JSONC (comments and trailing commas allowed), so the
// text is reduced to plain JSON before JSON.parse, then checked against
// RulesFileSchema, the Zod mirror of the published rules JSON Schema. The
// daemon validates again on save; this only gives the operator the error
// while they type.

import { RulesFileSchema, type Rule } from '../types/schemas';

/** Removes // and /* *\/ comments and trailing commas, outside strings. */
export function stripJsonc(text: string): string {
  return removeTrailingCommas(removeComments(text));
}

/** Length of the string literal starting at text[i] (a quote), quotes included. */
function stringLength(text: string, i: number): number {
  let j = i + 1;
  while (j < text.length && text[j] !== '"') j += text[j] === '\\' ? 2 : 1;
  return Math.min(j + 1, text.length) - i;
}

function removeComments(text: string): string {
  let out = '';
  let i = 0;
  while (i < text.length) {
    const c = text[i];
    if (c === '"') {
      const n = stringLength(text, i);
      out += text.slice(i, i + n);
      i += n;
    } else if (c === '/' && text[i + 1] === '/') {
      while (i < text.length && text[i] !== '\n') i++;
    } else if (c === '/' && text[i + 1] === '*') {
      const end = text.indexOf('*/', i + 2);
      out += ' ';
      i = end === -1 ? text.length : end + 2;
    } else {
      out += c;
      i++;
    }
  }
  return out;
}

function removeTrailingCommas(text: string): string {
  let out = '';
  let i = 0;
  while (i < text.length) {
    const c = text[i];
    if (c === '"') {
      const n = stringLength(text, i);
      out += text.slice(i, i + n);
      i += n;
      continue;
    }
    if (c === ',') {
      let j = i + 1;
      while (j < text.length && /\s/.test(text[j])) j++;
      if (text[j] === '}' || text[j] === ']') {
        i++;
        continue;
      }
    }
    out += c;
    i++;
  }
  return out;
}

export type RulesTextResult = { ok: true; rules: Rule[] } | { ok: false; error: string };

/** Parses and validates the JSON view's text. Blank text is a file with no rules. */
export function parseRulesText(text: string): RulesTextResult {
  if (text.trim() === '') return { ok: true, rules: [] };
  let value: unknown;
  try {
    value = JSON.parse(stripJsonc(text));
  } catch (err) {
    return {
      ok: false,
      error: `Not valid JSON: ${err instanceof Error ? err.message : 'parse error'}`,
    };
  }
  const parsed = RulesFileSchema.safeParse(value);
  if (!parsed.success) {
    const issue = parsed.error.issues[0];
    const at = issue.path.length > 0 ? issue.path.map(String).join('.') : 'file';
    return { ok: false, error: `${at}: ${issue.message}` };
  }
  return { ok: true, rules: parsed.data.rules ?? [] };
}
