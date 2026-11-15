import { useEditorPrefsStore, type EditorChoice } from '../../store/editorPrefsStore';
import { EDITOR_LABEL } from '../../utils/editorLinks';

const CHOICES: readonly EditorChoice[] = ['vscode', 'cursor'];

/**
 * #238 — which editor the Changes tab's "Open in editor" links open. Per
 * browser; the links only appear when the dashboard runs on the daemon's
 * machine (a remote daemon shows a copyable path instead).
 */
export function EditorCard() {
  const editor = useEditorPrefsStore((s) => s.editor);
  const setEditor = useEditorPrefsStore((s) => s.setEditor);
  return (
    <div className="border-theme-line bg-theme-panel flex flex-wrap items-center justify-between gap-4 rounded-lg border p-4">
      <div className="min-w-0">
        <p className="text-theme-text text-sm font-medium">Open files in</p>
        <p className="text-theme-muted mt-0.5 text-xs">
          The editor that the Changes tab&apos;s file and line links open, for a worktree on this
          machine. A remote daemon shows a path to copy instead. Stored in this browser only.
        </p>
      </div>
      <div role="radiogroup" aria-label="Editor" className="flex gap-3 text-sm">
        {CHOICES.map((c) => (
          <label key={c} className="flex items-center gap-1">
            <input
              type="radio"
              name="editor"
              value={c}
              checked={editor === c}
              onChange={() => {
                setEditor(c);
              }}
            />
            {EDITOR_LABEL[c]}
          </label>
        ))}
      </div>
    </div>
  );
}
