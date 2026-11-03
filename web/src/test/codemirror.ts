// Test helpers for the CodeMirror prompt editor (#122). jsdom has no layout,
// so tests drive the EditorView with transactions and read state.doc instead
// of typing into the contenteditable.

import { act, screen } from '@testing-library/react';
import { EditorView } from '@codemirror/view';

/** The EditorView behind the editor with the given accessible name. */
export async function findEditor(name: string | RegExp): Promise<EditorView> {
  const content = await screen.findByRole('textbox', { name });
  const view = EditorView.findFromDOM(content);
  if (!view) throw new Error(`no EditorView for ${String(name)}`);
  return view;
}

/** Types text at the caret as a user would (fires activate-on-typing). */
export function typeInEditor(view: EditorView, text: string): void {
  act(() => {
    for (const ch of text) {
      const { from, to } = view.state.selection.main;
      view.dispatch({
        changes: { from, to, insert: ch },
        selection: { anchor: from + ch.length },
        userEvent: 'input.type',
      });
    }
  });
}

/** Replaces the whole document, as select-all + paste would. */
export function replaceEditorText(view: EditorView, text: string): void {
  act(() => {
    view.dispatch({
      changes: { from: 0, to: view.state.doc.length, insert: text },
      selection: { anchor: text.length },
      userEvent: 'input.paste',
    });
  });
}

/** Moves the caret without editing. */
export function moveCaret(view: EditorView, pos: number): void {
  act(() => {
    view.dispatch({ selection: { anchor: pos }, userEvent: 'select' });
  });
}

/** Lets the completion list pass its interaction delay so keys act on it. */
export async function settleCompletion(): Promise<void> {
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 120));
  });
}
