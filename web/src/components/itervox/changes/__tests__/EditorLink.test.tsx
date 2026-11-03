import { describe, it, expect, beforeEach } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { EditorLink } from '../EditorLink';
import { EditorCard } from '../../../../pages/Settings/EditorCard';
import { useEditorPrefsStore } from '../../../../store/editorPrefsStore';
import { editorHref, isLoopbackHost, worktreeFilePath } from '../../../../utils/editorLinks';

describe('open in editor (#238)', () => {
  beforeEach(() => {
    useEditorPrefsStore.setState({ editor: 'vscode' });
  });

  it('deep-links a local worktree file and line to the chosen editor', async () => {
    render(
      <>
        <EditorCard />
        <EditorLink root="/work/ENG 1/" path="src/a.go" line={12} hostname="localhost" />
      </>,
    );
    expect(screen.getByTestId('editor-link')).toHaveAttribute(
      'href',
      'vscode://file/work/ENG%201/src/a.go:12',
    );
    expect(screen.getByTestId('editor-link')).toHaveTextContent('Open in VS Code');
    await userEvent.click(screen.getByRole('radio', { name: 'Cursor' }));
    expect(screen.getByTestId('editor-link')).toHaveAttribute(
      'href',
      'cursor://file/work/ENG%201/src/a.go:12',
    );
    expect(localStorage.getItem('itervox.editor')).toBe('cursor');
  });

  it('shows a copyable path for a remote daemon', () => {
    render(<EditorLink root="/srv/work/ENG-1" path="a.go" line={3} hostname="build-box.lan" />);
    expect(screen.queryByTestId('editor-link')).toBeNull();
    expect(screen.getByTestId('editor-path')).toHaveTextContent('/srv/work/ENG-1/a.go:3');
    expect(screen.getByRole('button', { name: 'Copy the path of a.go' })).toBeInTheDocument();
  });

  it('renders nothing without a worktree path', () => {
    const { container } = render(<EditorLink path="a.go" hostname="localhost" />);
    expect(container).toBeEmptyDOMElement();
  });

  it('builds paths and URLs', () => {
    expect(isLoopbackHost('127.0.0.1')).toBe(true);
    expect(isLoopbackHost('[::1]')).toBe(true);
    expect(isLoopbackHost('app.localhost')).toBe(true);
    expect(isLoopbackHost('10.0.0.5')).toBe(false);
    expect(worktreeFilePath('/w/', '/a.go')).toBe('/w/a.go');
    expect(editorHref('vscode', 'C:\\w\\a.go')).toBe('vscode://file/C:/w/a.go');
  });
});
