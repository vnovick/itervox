import { describe, it, expect, vi } from 'vitest';
import { render, screen, fireEvent, waitFor } from '@testing-library/react';
import { AgentInfoModal } from '../AgentInfoModal';

vi.mock('../../ui/modal', () => ({
  Modal: ({ isOpen, children }: { isOpen: boolean; children: React.ReactNode }) =>
    isOpen ? <div data-testid="modal">{children}</div> : null,
}));

vi.mock('../../../pages/Settings/profiles/ProfileEditorFields', () => ({
  ProfileEditorFields: ({
    backend,
    command,
    prompt,
  }: {
    backend: string;
    command: string;
    prompt: string;
  }) => (
    <div data-testid="profile-editor-fields">
      <span data-testid="editor-backend">{backend}</span>
      <span data-testid="editor-command">{command}</span>
      <span data-testid="editor-prompt">{prompt}</span>
    </div>
  ),
}));

vi.mock('../../../pages/Settings/profiles/profileBadges', () => ({
  backendLabel: (b: string) => (b === 'codex' ? 'Codex' : 'Claude'),
  backendBadgeClass: () => 'badge-class',
}));

describe('AgentInfoModal', () => {
  it('renders nothing when profileName is null', () => {
    render(<AgentInfoModal profileName={null} onClose={vi.fn()} />);
    expect(screen.queryByTestId('modal')).toBeNull();
  });

  it('renders profile name as heading', () => {
    render(<AgentInfoModal profileName="reviewer" onClose={vi.fn()} />);
    expect(screen.getByText('reviewer')).toBeInTheDocument();
  });

  it('shows backend badge when profileDef has backend', () => {
    render(
      <AgentInfoModal
        profileName="reviewer"
        profileDef={{ command: 'claude', backend: 'claude', prompt: '' }}
        onClose={vi.fn()}
      />,
    );
    expect(screen.getByText('Claude')).toBeInTheDocument();
  });

  it('shows prompt text when available', () => {
    render(
      <AgentInfoModal
        profileName="reviewer"
        profileDef={{ command: 'claude', prompt: 'You are a code reviewer.' }}
        onClose={vi.fn()}
      />,
    );
    expect(screen.getByText('You are a code reviewer.')).toBeInTheDocument();
  });

  it('shows fallback message when no profile files are configured', () => {
    render(
      <AgentInfoModal
        profileName="reviewer"
        profileDef={{ command: 'claude' }}
        onClose={vi.fn()}
      />,
    );
    expect(screen.getByText(/No profile files configured/)).toBeInTheDocument();
  });

  describe('edit mode', () => {
    const profileDef = {
      command: 'claude',
      backend: 'claude' as const,
      prompt: 'Review code carefully.',
    };

    it('does not show Edit Profile button when onSave is not provided', () => {
      render(<AgentInfoModal profileName="reviewer" profileDef={profileDef} onClose={vi.fn()} />);
      expect(screen.queryByText('Edit Profile')).toBeNull();
    });

    it('shows Edit Profile button when onSave is provided', () => {
      const onSave = vi.fn().mockResolvedValue(undefined);
      render(
        <AgentInfoModal
          profileName="reviewer"
          profileDef={profileDef}
          onClose={vi.fn()}
          onSave={onSave}
        />,
      );
      expect(screen.getByText('Edit Profile')).toBeInTheDocument();
    });

    it('clicking Edit Profile shows editor fields and Save/Cancel buttons', () => {
      const onSave = vi.fn().mockResolvedValue(undefined);
      render(
        <AgentInfoModal
          profileName="reviewer"
          profileDef={profileDef}
          onClose={vi.fn()}
          onSave={onSave}
        />,
      );

      fireEvent.click(screen.getByText('Edit Profile'));

      expect(screen.getByTestId('profile-editor-fields')).toBeInTheDocument();
      expect(screen.getByText('Save')).toBeInTheDocument();
      expect(screen.getByText('Cancel')).toBeInTheDocument();
      // Edit Profile button should be gone in edit mode
      expect(screen.queryByText('Edit Profile')).toBeNull();
    });

    it('Cancel resets form and exits edit mode', () => {
      const onSave = vi.fn().mockResolvedValue(undefined);
      render(
        <AgentInfoModal
          profileName="reviewer"
          profileDef={profileDef}
          onClose={vi.fn()}
          onSave={onSave}
        />,
      );

      fireEvent.click(screen.getByText('Edit Profile'));
      expect(screen.getByTestId('profile-editor-fields')).toBeInTheDocument();

      fireEvent.click(screen.getByText('Cancel'));

      // Should be back in view mode
      expect(screen.queryByTestId('profile-editor-fields')).toBeNull();
      expect(screen.getByText('Edit Profile')).toBeInTheDocument();
    });

    it('Save button calls onSave with profile name and definition', async () => {
      const onSave = vi.fn().mockResolvedValue(undefined);
      render(
        <AgentInfoModal
          profileName="reviewer"
          profileDef={profileDef}
          onClose={vi.fn()}
          onSave={onSave}
        />,
      );

      fireEvent.click(screen.getByText('Edit Profile'));
      fireEvent.click(screen.getByText('Save'));

      await waitFor(() => {
        expect(onSave).toHaveBeenCalledTimes(1);
      });

      // First arg is the profile name
      expect(onSave.mock.calls[0][0]).toBe('reviewer');
      // Second arg is a ProfileDef object with command, backend, prompt
      const savedDef = onSave.mock.calls[0][1];
      expect(savedDef).toHaveProperty('command');
      expect(savedDef).toHaveProperty('backend');
    });

    it('does not preserve createIssueState when create_issue is not allowed', async () => {
      const onSave = vi.fn().mockResolvedValue(undefined);
      render(
        <AgentInfoModal
          profileName="reviewer"
          profileDef={{
            command: 'claude',
            backend: 'claude',
            prompt: 'Review code carefully.',
            createIssueState: 'Todo',
          }}
          onClose={vi.fn()}
          onSave={onSave}
        />,
      );

      fireEvent.click(screen.getByText('Edit Profile'));
      fireEvent.click(screen.getByText('Save'));

      await waitFor(() => {
        expect(onSave).toHaveBeenCalledTimes(1);
      });

      expect(onSave.mock.calls[0][1].createIssueState).toBeUndefined();
    });

    it('exits edit mode after successful save', async () => {
      const onSave = vi.fn().mockResolvedValue(undefined);
      render(
        <AgentInfoModal
          profileName="reviewer"
          profileDef={profileDef}
          onClose={vi.fn()}
          onSave={onSave}
        />,
      );

      fireEvent.click(screen.getByText('Edit Profile'));
      fireEvent.click(screen.getByText('Save'));

      await waitFor(() => {
        expect(screen.queryByTestId('profile-editor-fields')).toBeNull();
      });
      expect(screen.getByText('Edit Profile')).toBeInTheDocument();
    });
  });

  describe('model badge', () => {
    it('displays model badge when command has --model flag', () => {
      render(
        <AgentInfoModal
          profileName="coder"
          profileDef={{ command: 'claude --model claude-sonnet-4-6', backend: 'claude' }}
          onClose={vi.fn()}
        />,
      );
      // modelLabel returns the label from CLAUDE_MODELS or falls back to the model id
      expect(screen.getByText('Sonnet 4.6 - Balanced')).toBeInTheDocument();
    });

    it('does not display model badge when command has no --model flag', () => {
      render(
        <AgentInfoModal
          profileName="coder"
          profileDef={{ command: 'claude', backend: 'claude' }}
          onClose={vi.fn()}
        />,
      );
      // The model badge has bg-theme-bg-soft class; there should be no element with model text
      // Just verify agent-info-content renders without a model badge span
      const content = screen.getByTestId('agent-info-content');
      // Only the backend badge should exist, not a model badge
      const badges = content.querySelectorAll('span.rounded-full');
      // One badge for backend
      expect(badges.length).toBe(1);
    });
  });

  describe('markdown rendering', () => {
    it('renders prompt through ReactMarkdown', () => {
      render(
        <AgentInfoModal
          profileName="reviewer"
          profileDef={{ command: 'claude', prompt: '**Bold text** and `inline code`' }}
          onClose={vi.fn()}
        />,
      );
      // ReactMarkdown renders **Bold text** as <strong>
      expect(screen.getByText('Bold text')).toBeInTheDocument();
      expect(screen.getByText('inline code')).toBeInTheDocument();
    });
  });
});
