import { CopyButton } from '../../ui/CopyButton';
import { useEditorPrefsStore } from '../../../store/editorPrefsStore';
import {
  EDITOR_LABEL,
  editorHref,
  isLoopbackHost,
  worktreeFilePath,
} from '../../../utils/editorLinks';

/**
 * #238 — "Open in editor" for a worktree file (and line): a vscode:// or
 * cursor:// deep link (the editor chosen in Settings) when the dashboard
 * runs on the daemon's machine, else the path to copy. Renders nothing
 * without a worktree path. `hostname` is injectable for tests.
 */
export function EditorLink({
  root,
  path,
  line,
  hostname = window.location.hostname,
}: {
  root?: string;
  path: string;
  line?: number;
  hostname?: string;
}) {
  const editor = useEditorPrefsStore((s) => s.editor);
  if (!root) return null;
  const abs = worktreeFilePath(root, path);
  const where = line ? `${abs}:${String(line)}` : abs;
  if (isLoopbackHost(hostname)) {
    return (
      <a
        href={editorHref(editor, abs, line)}
        className="text-theme-accent-text text-xs hover:underline"
        data-testid="editor-link"
      >
        Open in {EDITOR_LABEL[editor]}
      </a>
    );
  }
  return (
    <span className="flex min-w-0 items-center gap-1 text-xs" data-testid="editor-path">
      <code className="text-theme-text-secondary truncate font-mono" title={where}>
        {where}
      </code>
      <CopyButton text={where} label={`Copy the path of ${path}`} />
    </span>
  );
}
