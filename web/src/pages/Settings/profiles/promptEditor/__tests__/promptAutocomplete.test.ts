import { afterEach, describe, expect, it } from 'vitest';
import { CompletionContext as CMContext, type CompletionResult } from '@codemirror/autocomplete';
import { EditorState } from '@codemirror/state';
import { EditorView } from '@codemirror/view';
import {
  defaultSuggestionProvider,
  insertSuggestion,
  promptCompletionSource,
  type PromptSuggestionProvider,
} from '../promptAutocomplete';
import {
  LIQUID_FILTERS,
  PROFILE_VARIABLES,
  completionEdit,
  type Completion,
} from '../../promptCompletions';

const tdd: Completion = { label: 'tdd', kind: 'skill', source: 'project', detail: 'Test first' };
const reviewer: Completion = { label: 'code-reviewer', kind: 'subagent' };

let views: EditorView[] = [];
afterEach(() => {
  for (const v of views) v.destroy();
  views = [];
});

function viewAt(doc: string, caret: number): EditorView {
  const view = new EditorView({
    state: EditorState.create({ doc, selection: { anchor: caret } }),
    parent: document.body,
  });
  views.push(view);
  return view;
}

function query(provider: PromptSuggestionProvider, doc: string): CompletionResult | null {
  const source = promptCompletionSource(() => provider);
  const state = EditorState.create({ doc });
  return source(new CMContext(state, doc.length, false)) as CompletionResult | null;
}

describe('promptCompletionSource (#122)', () => {
  const provider = defaultSuggestionProvider([tdd, reviewer], PROFILE_VARIABLES);

  it('answers from the context start with the #87 order and no CodeMirror filtering', () => {
    const result = query(provider, 'Use /');
    expect(result).toMatchObject({ from: 5, to: 5, filter: false });
    expect(result?.options.map((o) => [o.label, o.detail, o.info])).toEqual([
      ['tdd', 'skill · project', 'Test first'],
      ['code-reviewer', 'subagent', undefined],
    ]);
  });

  it('offers variables after {{ and filters after |', () => {
    expect(query(provider, '{{ issue.ti')?.options[0].label).toBe('issue.title');
    expect(query(provider, '{{ x | ')?.options.map((o) => o.label)).toEqual(
      LIQUID_FILTERS.slice(0, 8).map((f) => f.label),
    );
  });

  it('returns nothing outside a context or when nothing matches', () => {
    expect(query(provider, 'plain text')).toBeNull();
    expect(query(provider, '/zzz')).toBeNull();
  });

  it('asks the live provider every time (pluggable for #123)', () => {
    let current: PromptSuggestionProvider = () => [tdd];
    const source = promptCompletionSource(() => current);
    const state = EditorState.create({ doc: '/' });
    const ask = () =>
      (source(new CMContext(state, 1, false)) as CompletionResult | null)?.options.map(
        (o) => o.label,
      );
    expect(ask()).toEqual(['tdd']);
    current = () => [reviewer];
    expect(ask()).toEqual(['code-reviewer']);
  });
});

describe('insertSuggestion (#122)', () => {
  it('applies the #87 insertion rules as one edit', () => {
    const view = viewAt('Title: {{ issue.ti', 18);
    insertSuggestion(view, PROFILE_VARIABLES[1]);
    expect(view.state.doc.toString()).toBe('Title: {{ issue.title }}');
    expect(view.state.selection.main.head).toBe(24);
  });

  it('replaces a selection and the rest of the word', () => {
    const view = viewAt('Use /tdx now', 5);
    view.dispatch({ selection: { anchor: 5, head: 7 } });
    insertSuggestion(view, tdd);
    expect(view.state.doc.toString()).toBe('Use /tdd now');
  });

  it('inserts nothing when the caret left the context', () => {
    const view = viewAt('Use /td', 4);
    insertSuggestion(view, tdd);
    expect(view.state.doc.toString()).toBe('Use /td');
  });
});

describe('completionEdit (#122)', () => {
  it('returns the replaced range and the caret', () => {
    const ctx = { kind: 'reference' as const, query: 't', start: 5, trigger: '/' as const };
    expect(completionEdit('Use /td more', 6, ctx, tdd)).toEqual({
      from: 5,
      to: 7,
      insert: 'tdd',
      caret: 8,
    });
  });
});
