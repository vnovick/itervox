import { create } from 'zustand';

// #238 — which editor "Open in editor" links open: VS Code or Cursor. Per
// browser (local storage), default VS Code; storage failures (private
// mode, blocked site data) fall back to in-memory state.
const KEY = 'itervox.editor';

export type EditorChoice = 'vscode' | 'cursor';

function readEditor(): EditorChoice {
  try {
    return localStorage.getItem(KEY) === 'cursor' ? 'cursor' : 'vscode';
  } catch {
    return 'vscode';
  }
}

interface EditorPrefsState {
  editor: EditorChoice;
  setEditor: (editor: EditorChoice) => void;
}

export const useEditorPrefsStore = create<EditorPrefsState>((set) => ({
  editor: readEditor(),
  setEditor: (editor) => {
    try {
      localStorage.setItem(KEY, editor);
    } catch {
      // in-memory only
    }
    set({ editor });
  },
}));
