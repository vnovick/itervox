import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import { ProfilesCard } from '../ProfilesCard';
import { ModelServerField } from '../profiles/ModelServerField';
import { modelServerProblem } from '../profiles/modelServer';

vi.mock('../../../queries/skills', () => ({
  useSkillsInventory: () => ({ data: null }),
}));

function renderCard(modelServer?: string) {
  const onUpsert = vi.fn().mockResolvedValue(true);
  render(
    <ProfilesCard
      profileDefs={{
        local: {
          command: 'claude',
          backend: 'claude',
          prompt: 'Work locally.',
          enabled: true,
          ...(modelServer !== undefined ? { modelServer } : {}),
        },
      }}
      onUpsert={onUpsert}
      onDelete={vi.fn().mockResolvedValue(true)}
    />,
  );
  return onUpsert;
}

async function openEditor(user: ReturnType<typeof userEvent.setup>) {
  const card = screen.getByText('local').closest('article');
  expect(card).not.toBeNull();
  await user.click(within(card as HTMLElement).getByRole('button', { name: 'Edit' }));
}

describe('Model server field', () => {
  it('shows the saved model server and sends an edited one through the profile save', async () => {
    const user = userEvent.setup();
    const onUpsert = renderCard('http://127.0.0.1:11434');
    await openEditor(user);

    const input = screen.getByLabelText('Model server');
    expect(input).toHaveValue('http://127.0.0.1:11434');
    expect(input).toHaveAttribute('placeholder', 'http://127.0.0.1:<port> or $LOCAL_MODEL_URL');
    expect(screen.getByText('ANTHROPIC_BASE_URL')).toBeInTheDocument();

    await user.clear(input);
    await user.type(input, '$LOCAL_MODEL_URL');
    await user.click(screen.getByRole('button', { name: 'Save Changes' }));

    await waitFor(() => {
      expect(onUpsert).toHaveBeenCalledTimes(1);
    });
    const call = onUpsert.mock.calls[0];
    expect(call[0]).toBe('local');
    expect(call[11]).toBe('local');
    expect(call[12]).toBe('$LOCAL_MODEL_URL');
  });

  it('refuses a value that is not a URL or a whole variable, without saving', async () => {
    const user = userEvent.setup();
    const onUpsert = renderCard();
    await openEditor(user);

    const input = screen.getByLabelText('Model server');
    expect(input).toHaveValue('');
    await user.type(input, '127.0.0.1:11434');
    await user.click(screen.getByRole('button', { name: 'Save Changes' }));

    expect(await screen.findByText(/Enter an http:\/\/ or https:\/\/ URL/)).toBeInTheDocument();
    expect(onUpsert).not.toHaveBeenCalled();
  });

  it('refuses a URL without an explicit port, without saving', async () => {
    const user = userEvent.setup();
    const onUpsert = renderCard();
    await openEditor(user);
    await user.type(screen.getByLabelText('Model server'), 'http://127.0.0.1/v1');
    await user.click(screen.getByRole('button', { name: 'Save Changes' }));
    expect(await screen.findByText(/Add the port the server listens on/)).toBeInTheDocument();
    expect(onUpsert).not.toHaveBeenCalled();
  });

  it('clearing the field sends an empty value, which clears it on the daemon', async () => {
    const user = userEvent.setup();
    const onUpsert = renderCard('http://127.0.0.1:1234');
    await openEditor(user);
    await user.clear(screen.getByLabelText('Model server'));
    await user.click(screen.getByRole('button', { name: 'Save Changes' }));
    await waitFor(() => {
      expect(onUpsert).toHaveBeenCalledTimes(1);
    });
    expect(onUpsert.mock.calls[0][12]).toBe('');
  });

  it('names the variable per backend and explains backends that read their own config', () => {
    const { rerender } = render(
      <ModelServerField backend="codex" value="http://127.0.0.1:1234/v1" onChange={vi.fn()} />,
    );
    expect(screen.getByText('OPENAI_BASE_URL')).toBeInTheDocument();
    expect(screen.getByText(/own quota pool and longer timeouts/)).toBeInTheDocument();
    rerender(<ModelServerField backend="opencode" value="" onChange={vi.fn()} />);
    expect(
      screen.getByText(/OpenCode reads its model server from its own config/),
    ).toBeInTheDocument();
  });

  it('modelServerProblem mirrors the daemon check', () => {
    expect(modelServerProblem('', 'opencode')).toBeNull();
    expect(modelServerProblem('http://127.0.0.1:11434', 'claude')).toBeNull();
    expect(modelServerProblem('https://box.lan:443/v1', 'codex')).toBeNull();
    expect(modelServerProblem('http://user@[::1]:1234/v1', 'claude')).toBeNull();
    expect(modelServerProblem('http://box.lan:80', 'claude')).toBeNull();
    expect(modelServerProblem('$LOCAL_MODEL_URL', 'claude')).toBeNull();
    expect(modelServerProblem('http://$HOST:1234', 'claude')).toMatch(/whole value/);
    expect(modelServerProblem('ftp://box', 'claude')).toMatch(/http/);
    expect(modelServerProblem('https://box.lan/v1', 'codex')).toMatch(/Add the port/);
    expect(modelServerProblem('http://[::1]/', 'claude')).toMatch(/Add the port/);
    expect(modelServerProblem('http://127.0.0.1:', 'claude')).toMatch(/Add the port/);
    expect(modelServerProblem('http://127.0.0.1:1234', 'cursor')).toMatch(/Claude and Codex/);
  });
});
