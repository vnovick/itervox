import { fireEvent, render, screen } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import { ProfileEditorFields } from '../profiles/ProfileEditorFields';

describe('ProfileEditorFields', () => {
  it('renders separate SOUL and INSTRUCTIONS editors for file-backed profiles', () => {
    render(
      <ProfileEditorFields
        backend="claude"
        model=""
        command="claude"
        prompt=""
        soul="# Reviewer SOUL"
        instructions="# Reviewer INSTRUCTIONS"
        soulFile=".itervox/agents/reviewer/SOUL.md"
        instructionsFile=".itervox/agents/reviewer/INSTRUCTIONS.md"
        allowedActions={[]}
        createIssueState=""
        trackerStates={['Todo']}
        onBackendChange={vi.fn()}
        onModelChange={vi.fn()}
        onCommandChange={vi.fn()}
        onPromptChange={vi.fn()}
        onSoulChange={vi.fn()}
        onInstructionsChange={vi.fn()}
        onAllowedActionsChange={vi.fn()}
        onCreateIssueStateChange={vi.fn()}
      />,
    );

    expect(screen.getByText('SOUL.md')).toBeInTheDocument();
    expect(screen.getByText('INSTRUCTIONS.md')).toBeInTheDocument();
    expect(screen.getByText('.itervox/agents/reviewer/SOUL.md')).toBeInTheDocument();
    expect(screen.getByText('.itervox/agents/reviewer/INSTRUCTIONS.md')).toBeInTheDocument();
    expect(screen.queryByText('Prompt')).not.toBeInTheDocument();
  });

  it('places daemon actions and prompt variables above a prompt editor with preview controls', () => {
    const { container } = render(
      <ProfileEditorFields
        backend="claude"
        model=""
        command="claude"
        prompt="## Review\n\n**Use {{ issue.title }}.**"
        allowedActions={['comment']}
        createIssueState=""
        trackerStates={['Todo', 'In Progress']}
        onBackendChange={vi.fn()}
        onModelChange={vi.fn()}
        onCommandChange={vi.fn()}
        onPromptChange={vi.fn()}
        onAllowedActionsChange={vi.fn()}
        onCreateIssueStateChange={vi.fn()}
      />,
    );

    const actionsHeading = screen.getByText('Daemon Actions');
    const variablesHeading = screen.getByText('Prompt variables');
    const promptLabel = screen.getByText('Prompt');
    const writeButton = screen.getByRole('button', { name: 'Write' });
    const previewButton = screen.getByRole('button', { name: 'Preview' });

    expect(
      actionsHeading.compareDocumentPosition(promptLabel) & Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy();
    expect(
      variablesHeading.compareDocumentPosition(promptLabel) & Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy();
    expect(writeButton).toBeInTheDocument();
    expect(previewButton).toBeInTheDocument();

    fireEvent.click(previewButton);

    expect(container.querySelector('strong')).toHaveTextContent('Use {{ issue.title }}.');
  });

  it('shows a target state field when create issue is allowed', () => {
    render(
      <ProfileEditorFields
        backend="claude"
        model=""
        command="claude"
        prompt=""
        allowedActions={['create_issue']}
        createIssueState="Todo"
        trackerStates={['Todo', 'Backlog']}
        onBackendChange={vi.fn()}
        onModelChange={vi.fn()}
        onCommandChange={vi.fn()}
        onPromptChange={vi.fn()}
        onAllowedActionsChange={vi.fn()}
        onCreateIssueStateChange={vi.fn()}
      />,
    );

    expect(screen.getByLabelText('Follow-up issue state')).toHaveValue('Todo');
  });

  // Gap §4.5 — verify the new comment_pr action option renders in the daemon
  // actions list. Without this assertion, a rename of the option id or a
  // missing import would silently disappear from the editor UI.
  it('renders the comment_pr ("Post structured review") option in Daemon Actions', () => {
    render(
      <ProfileEditorFields
        backend="claude"
        model=""
        command="claude"
        prompt=""
        allowedActions={[]}
        createIssueState=""
        trackerStates={['Todo']}
        onBackendChange={vi.fn()}
        onModelChange={vi.fn()}
        onCommandChange={vi.fn()}
        onPromptChange={vi.fn()}
        onAllowedActionsChange={vi.fn()}
        onCreateIssueStateChange={vi.fn()}
      />,
    );
    expect(screen.getByText('Post structured review')).toBeInTheDocument();
  });

  it('filters daemon action checkboxes to backend-supported actions', () => {
    render(
      <ProfileEditorFields
        backend="claude"
        model=""
        command="claude"
        prompt=""
        allowedActions={[]}
        supportedAgentActions={['comment']}
        createIssueState=""
        trackerStates={['Todo']}
        onBackendChange={vi.fn()}
        onModelChange={vi.fn()}
        onCommandChange={vi.fn()}
        onPromptChange={vi.fn()}
        onAllowedActionsChange={vi.fn()}
        onCreateIssueStateChange={vi.fn()}
      />,
    );

    expect(screen.getByText('Comment on current issue')).toBeInTheDocument();
    expect(screen.queryByText('Post structured review')).not.toBeInTheDocument();
  });
});
