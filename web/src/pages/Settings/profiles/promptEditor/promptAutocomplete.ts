// The #87 suggestions on @codemirror/autocomplete (#122). Which context the
// caret is in, what to suggest and how a choice is inserted all stay in
// ../promptCompletions.ts; this file only adapts them to CodeMirror, which
// owns the list, the keyboard (arrows, Enter, Escape, Ctrl-Space) and the
// listbox ARIA wiring.
//
// The candidate provider is pluggable: the editor passes a getter, so a
// richer provider (backend-aware suggestions, #123) can replace the default
// one without touching the editor or this adapter.

import {
  autocompletion,
  closeCompletion,
  pickedCompletion,
  type Completion as CMCompletion,
  type CompletionContext as CMContext,
  type CompletionResult,
  type CompletionSource,
} from '@codemirror/autocomplete';
import { Prec, type Extension } from '@codemirror/state';
import { keymap, type EditorView } from '@codemirror/view';
import {
  candidatesFor,
  completionContext,
  completionEdit,
  matchCompletions,
  type Completion,
  type CompletionContext,
} from '../promptCompletions';

/** Returns every candidate for a context; matching and ranking happen after. */
export type PromptSuggestionProvider = (ctx: CompletionContext) => readonly Completion[];

/** The default provider: inventory references, the editor's variables, and the Liquid filters. */
export function defaultSuggestionProvider(
  references: readonly Completion[],
  variables: readonly Completion[],
): PromptSuggestionProvider {
  return (ctx) => candidatesFor(ctx, references, variables);
}

const KIND_LABEL: Record<Completion['kind'], string> = {
  skill: 'skill',
  subagent: 'subagent',
  variable: 'variable',
  filter: 'filter',
};

/**
 * Inserts item at the caret. The context is worked out again from the caret
 * now: if it moved since the list opened, the list is stale and nothing is
 * inserted (the list just closes).
 */
export function insertSuggestion(view: EditorView, item: Completion, picked?: CMCompletion): void {
  const { state } = view;
  const sel = state.selection.main;
  const text = state.doc.toString();
  const ctx = completionContext(text, sel.from);
  if (!ctx) {
    closeCompletion(view);
    return;
  }
  const edit = completionEdit(text, sel.from, ctx, item, sel.to);
  view.dispatch({
    changes: { from: edit.from, to: edit.to, insert: edit.insert },
    selection: { anchor: edit.caret },
    scrollIntoView: true,
    userEvent: 'input.complete',
    annotations: picked ? pickedCompletion.of(picked) : undefined,
  });
}

function toOption(item: Completion): CMCompletion {
  return {
    label: item.label,
    detail: KIND_LABEL[item.kind] + (item.source ? ` · ${item.source}` : ''),
    info: item.detail,
    type: item.kind === 'subagent' ? 'class' : item.kind === 'skill' ? 'function' : 'variable',
    apply: (view, completion) => {
      insertSuggestion(view, item, completion);
    },
  };
}

/** The CodeMirror completion source over a (live) suggestion provider. */
export function promptCompletionSource(
  getProvider: () => PromptSuggestionProvider,
): CompletionSource {
  return (context: CMContext): CompletionResult | null => {
    const text = context.state.doc.toString();
    const ctx = completionContext(text, context.pos);
    if (!ctx) return null;
    const items = matchCompletions(getProvider()(ctx), ctx.query);
    if (items.length === 0) return null;
    // filter: false keeps the #87 order (prefix matches, then substring);
    // the source runs again on every keystroke.
    return { from: ctx.start, to: context.pos, options: items.map(toOption), filter: false };
  };
}

/**
 * The autocomplete extension. Tab is left to focus navigation (the dialog's
 * focus trap), and an Escape that closes the list stops there instead of
 * also closing the surrounding dialog.
 */
export function promptAutocomplete(getProvider: () => PromptSuggestionProvider): Extension {
  return [
    autocompletion({
      override: [promptCompletionSource(getProvider)],
      icons: false,
      maxRenderedOptions: 8,
    }),
    Prec.highest(keymap.of([{ key: 'Escape', run: closeCompletion, stopPropagation: true }])),
  ];
}
