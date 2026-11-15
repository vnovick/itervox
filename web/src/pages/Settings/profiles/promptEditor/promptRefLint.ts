// Unknown skill / subagent references underlined in the prompt editor
// (#123). The check itself is the daemon's #86 validator, reached through
// POST /api/v1/skills/check-refs, so the editor underlines exactly what
// `itervox doctor` reports — per backend, and with the SSH-host note when
// a reference exists only outside the repository. This file only turns the
// result into @codemirror/lint diagnostics (underline + hover message; F8
// and Mod-Shift-M reach them from the keyboard).

import { linter, lintKeymap, type Diagnostic } from '@codemirror/lint';
import { StateEffect, type Extension } from '@codemirror/state';
import { keymap } from '@codemirror/view';
import type { PromptRefCheck } from '../../../../types/schemas';

/** Checks one prompt; null means "no check for this editor". */
export type PromptRefChecker = (text: string, signal: AbortSignal) => Promise<PromptRefCheck>;

/** Re-runs the check now (the profile's command or backend changed). */
export const recheckPromptRefs = StateEffect.define();

/** How long typing must pause before the text is checked. */
export const PROMPT_REF_LINT_DELAY_MS = 400;

/** The diagnostics for a check result: one per reference that is not ok. */
export function promptRefDiagnostics(check: PromptRefCheck, docLength: number): Diagnostic[] {
  const out: Diagnostic[] = [];
  for (const ref of check.refs ?? []) {
    let severity: Diagnostic['severity'];
    if (ref.status === 'missing') severity = 'warning';
    else if (ref.status === 'outside_repo') severity = 'info';
    else continue; // ok, or a status this bundle does not know
    const from = Math.max(0, Math.min(ref.from, docLength));
    const to = Math.max(from, Math.min(ref.to, docLength));
    if (to === from) continue;
    out.push({
      from,
      to,
      severity,
      source: ref.issueId,
      message: ref.message ?? `Unknown ${ref.kind} "${ref.name}"`,
    });
  }
  return out;
}

/**
 * The lint extension. getChecker is read on every run, so the editor can
 * switch profiles without rebuilding the view. A failed check (no
 * inventory yet, offline) clears the underlines.
 */
export function promptRefLint(getChecker: () => PromptRefChecker | null): Extension {
  let inflight: AbortController | null = null;
  return [
    linter(
      async (view) => {
        inflight?.abort();
        const checker = getChecker();
        if (!checker) return [];
        const controller = new AbortController();
        inflight = controller;
        const text = view.state.doc.toString();
        try {
          const check = await checker(text, controller.signal);
          if (controller.signal.aborted) return [];
          return promptRefDiagnostics(check, view.state.doc.length);
        } catch {
          return [];
        }
      },
      {
        delay: PROMPT_REF_LINT_DELAY_MS,
        needsRefresh: (update) =>
          update.transactions.some((tr) => tr.effects.some((e) => e.is(recheckPromptRefs))),
      },
    ),
    keymap.of(lintKeymap),
  ];
}
