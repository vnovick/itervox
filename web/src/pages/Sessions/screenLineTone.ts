// Semantic tints for a tmux pane's text. Only lines whose meaning is known
// from their leading glyph are coloured (the agent CLIs mark finished steps
// with a check, failures with a cross and questions to the operator with
// "?"); everything else keeps the plain console colour.

export type ScreenLineTone = 'ok' | 'warn' | 'danger' | null;

const OK = /^[✓✔]/u;
const DANGER = /^[✗✘]/u;
const QUESTION = /^\?\s/u;

export function screenLineTone(line: string): ScreenLineTone {
  const head = line.trimStart();
  if (OK.test(head)) return 'ok';
  if (DANGER.test(head)) return 'danger';
  if (QUESTION.test(head)) return 'warn';
  return null;
}

export const SCREEN_TONE_CLASS: Record<Exclude<ScreenLineTone, null>, string> = {
  ok: 'text-theme-success-text',
  warn: 'text-theme-warning-text',
  danger: 'text-theme-danger-text',
};
