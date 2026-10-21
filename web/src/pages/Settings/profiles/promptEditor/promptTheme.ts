// The prompt editor's theme (#122). Every colour is a dashboard token
// (src/index.css), so light and dark follow [data-theme] with no second theme.

import { EditorView } from '@codemirror/view';

export const promptTheme = EditorView.theme({
  '&': {
    backgroundColor: 'var(--panel-strong)',
    color: 'var(--text)',
    border: '1px solid var(--line)',
    borderRadius: 'var(--radius-sm)',
    fontSize: '13px',
    maxHeight: '70vh',
  },
  '&.cm-focused': { outline: 'none', borderColor: 'var(--accent)' },
  '.cm-scroller': {
    fontFamily: 'var(--font-mono)',
    lineHeight: '1.5rem',
    overflow: 'auto',
  },
  '.cm-content': { minHeight: '420px', padding: '8px 0', caretColor: 'var(--text)' },
  '.cm-line': { padding: '0 12px' },
  '.cm-cursor, .cm-dropCursor': { borderLeftColor: 'var(--text)' },
  '&.cm-focused > .cm-scroller > .cm-selectionLayer .cm-selectionBackground, .cm-selectionBackground, ::selection':
    { backgroundColor: 'var(--accent-soft)' },
  '.cm-placeholder': { color: 'var(--muted)' },
  '.cm-searchMatch': { backgroundColor: 'var(--warning-soft)' },
  '.cm-searchMatch.cm-searchMatch-selected': { backgroundColor: 'var(--accent-soft)' },

  // Markdown
  '.cm-md-heading': { color: 'var(--accent-text)', fontWeight: '600' },
  '.cm-md-code': {
    color: 'var(--teal)',
    backgroundColor: 'var(--bg-soft)',
    borderRadius: '3px',
  },
  '.cm-md-emphasis': { fontStyle: 'italic' },
  '.cm-md-strong': { fontWeight: '600' },
  '.cm-md-link': { color: 'var(--accent-text)', textDecoration: 'underline' },
  '.cm-md-quote': { color: 'var(--text-secondary)', fontStyle: 'italic' },
  '.cm-md-mark, .cm-md-list': { color: 'var(--muted)' },

  // Liquid
  '.cm-liquid-object': {
    color: 'var(--purple)',
    backgroundColor: 'var(--purple-soft)',
    borderRadius: '3px',
  },
  '.cm-liquid-tag': {
    color: 'var(--warning-text)',
    backgroundColor: 'var(--warning-soft)',
    borderRadius: '3px',
  },

  // Autocomplete list
  '.cm-tooltip': {
    backgroundColor: 'var(--panel)',
    color: 'var(--text)',
    border: '1px solid var(--line-strong)',
    borderRadius: 'var(--radius-sm)',
  },
  '.cm-tooltip.cm-tooltip-autocomplete > ul': {
    fontFamily: 'var(--font-mono)',
    fontSize: '12px',
    maxHeight: '16rem',
    minWidth: '18rem',
  },
  '.cm-tooltip.cm-tooltip-autocomplete > ul > li': { padding: '4px 12px' },
  '.cm-tooltip-autocomplete ul li[aria-selected]': {
    backgroundColor: 'var(--accent-soft)',
    color: 'var(--text)',
  },
  '.cm-completionDetail': { color: 'var(--muted)', fontStyle: 'normal', marginLeft: '0.75em' },
  '.cm-completionInfo': { fontFamily: 'var(--font-sans)', fontSize: '12px', maxWidth: '20rem' },

  // Search panel
  '.cm-panels': { backgroundColor: 'var(--panel)', color: 'var(--text)' },
  '.cm-panels.cm-panels-top': { borderBottom: '1px solid var(--line)' },
  '.cm-panels.cm-panels-bottom': { borderTop: '1px solid var(--line)' },
  '.cm-panel input, .cm-panel button': {
    color: 'var(--text)',
    backgroundColor: 'var(--bg-soft)',
    border: '1px solid var(--line-strong)',
    borderRadius: '4px',
  },
});
