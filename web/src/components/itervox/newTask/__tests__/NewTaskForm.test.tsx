// #251 — the New task form: its fields, starting from a template (with
// `{{var}}` values), Create vs Create and start, and validation (client side
// and the daemon's field errors).
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor } from '@testing-library/react';
import { authedFetch } from '../../../../auth/authedFetch';
import { render } from '../../../../test/render';
import { makeSnapshot } from '../../../../test/fixtures/snapshots';
import { useUIStore } from '../../../../store/uiStore';
import { useNewTaskUrlOpen } from '../../../../hooks/useUrlState';
import { NewTaskForm } from '../NewTaskForm';
import { NewTaskDialog } from '../NewTaskDialog';
import { DashboardIssuesHeader } from '../../../../pages/Dashboard/components/DashboardIssuesHeader';
import { buildTaskRequest, EMPTY_DRAFT, fillVars, templateVars } from '../newTaskModel';

vi.mock('../../../../auth/authedFetch', () => ({ authedFetch: vi.fn() }));
const mockFetch = vi.mocked(authedFetch);

const templates = {
  localTasks: true,
  templates: [
    {
      name: 'upgrade',
      title: 'Upgrade {{package}}',
      description: 'Bump one dependency.',
      instructions: 'Upgrade {{package}} to {{version}} and run the tests.',
      profile: 'implementer',
      playbook: 'bug-fix',
      baseBranch: 'main',
      vars: ['package', 'version'],
    },
  ],
};

interface Call {
  url: string;
  method: string;
  body: unknown;
}
let calls: Call[] = [];
let createResponse: { status: number; body: unknown } = {
  status: 201,
  body: { identifier: 'TASK-1', title: 'x', state: 'Todo', local: true, started: false },
};

function json(status: number, body: unknown): Promise<Response> {
  return Promise.resolve(
    new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } }),
  );
}

function setup(localTasks = true) {
  calls = [];
  mockFetch.mockReset();
  mockFetch.mockImplementation((input: RequestInfo | URL, init?: RequestInit) => {
    const url = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;
    const method = init?.method ?? 'GET';
    const body: unknown = typeof init?.body === 'string' ? JSON.parse(init.body) : undefined;
    calls.push({ url, method, body });
    if (url === '/api/v1/task-templates') return json(200, { ...templates, localTasks });
    if (url === '/api/v1/playbooks')
      return json(200, {
        enabled: true,
        playbooks: [{ name: 'bug-fix', keywords: [], requireEvidence: [] }],
      });
    if (url === '/api/v1/tasks' && method === 'POST')
      return json(createResponse.status, createResponse.body);
    return json(404, {});
  });
}

const snapshot = makeSnapshot({ availableProfiles: ['implementer', 'reviewer'] });
const posts = () => calls.filter((c) => c.url === '/api/v1/tasks' && c.method === 'POST');

beforeEach(() => {
  createResponse = {
    status: 201,
    body: { identifier: 'TASK-1', title: 'x', state: 'Todo', local: true, started: false },
  };
  setup();
});

describe('NewTaskForm (#251)', () => {
  it('shows every field: template, title, instructions, where, profile, backend, playbook, base', async () => {
    render(<NewTaskForm onDone={vi.fn()} />, { snapshot });
    expect(await screen.findByRole('combobox', { name: 'Template' })).toBeInTheDocument();
    expect(screen.getByRole('textbox', { name: 'Title' })).toBeInTheDocument();
    expect(screen.getByRole('textbox', { name: 'Custom instructions' })).toBeInTheDocument();
    expect(screen.getByRole('radio', { name: 'Local issue (Itervox only)' })).toBeChecked();
    expect(screen.getByRole('radio', { name: 'Tracker issue' })).not.toBeChecked();
    expect(screen.getByRole('combobox', { name: 'Profile' })).toHaveTextContent('implementer');
    expect(screen.getByRole('combobox', { name: 'Backend' })).toHaveTextContent('codex');
    expect(await screen.findByRole('option', { name: 'bug-fix' })).toBeInTheDocument();
    expect(screen.getByRole('textbox', { name: 'Base branch' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Create' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Create and start' })).toBeInTheDocument();
  });

  it('creates a local task without starting it', async () => {
    const onDone = vi.fn();
    render(<NewTaskForm onDone={onDone} />, { snapshot });
    fireEvent.change(screen.getByRole('textbox', { name: 'Title' }), {
      target: { value: 'Add parser tests' },
    });
    fireEvent.change(screen.getByRole('textbox', { name: 'Custom instructions' }), {
      target: { value: 'Cover the error paths.' },
    });
    fireEvent.click(screen.getByRole('button', { name: 'Create' }));
    await waitFor(() => {
      expect(onDone).toHaveBeenCalled();
    });
    expect(posts()).toHaveLength(1);
    expect(posts()[0]?.body).toEqual({
      title: 'Add parser tests',
      instructions: 'Cover the error paths.',
      where: 'local',
      start: false,
    });
  });

  it('create and start sends start, the tracker choice and the options', async () => {
    const onDone = vi.fn();
    render(<NewTaskForm onDone={onDone} />, { snapshot });
    fireEvent.change(screen.getByRole('textbox', { name: 'Title' }), {
      target: { value: 'Upgrade lodash' },
    });
    fireEvent.click(screen.getByRole('radio', { name: 'Tracker issue' }));
    fireEvent.change(screen.getByRole('combobox', { name: 'Profile' }), {
      target: { value: 'implementer' },
    });
    fireEvent.change(screen.getByRole('combobox', { name: 'Backend' }), {
      target: { value: 'codex' },
    });
    fireEvent.change(screen.getByRole('textbox', { name: 'Base branch' }), {
      target: { value: 'release/2.x' },
    });
    fireEvent.click(screen.getByRole('button', { name: 'Create and start' }));
    await waitFor(() => {
      expect(onDone).toHaveBeenCalled();
    });
    expect(posts()[0]?.body).toEqual({
      title: 'Upgrade lodash',
      instructions: '',
      profile: 'implementer',
      backend: 'codex',
      baseBranch: 'release/2.x',
      where: 'tracker',
      start: true,
    });
  });

  it('starts from a template and fills its placeholders', async () => {
    render(<NewTaskForm onDone={vi.fn()} />, { snapshot });
    const pick = await screen.findByRole('combobox', { name: 'Template' });
    fireEvent.change(pick, { target: { value: 'upgrade' } });
    expect(screen.getByRole('textbox', { name: 'Title' })).toHaveValue('Upgrade {{package}}');
    expect(screen.getByRole('combobox', { name: 'Profile' })).toHaveValue('implementer');
    expect(screen.getByRole('textbox', { name: 'Base branch' })).toHaveValue('main');

    // A placeholder without a value blocks the create.
    fireEvent.click(screen.getByRole('button', { name: 'Create' }));
    expect(await screen.findByText('Fill in: package, version')).toBeInTheDocument();
    expect(posts()).toHaveLength(0);

    fireEvent.change(screen.getByRole('textbox', { name: 'Value for package' }), {
      target: { value: 'lodash' },
    });
    fireEvent.change(screen.getByRole('textbox', { name: 'Value for version' }), {
      target: { value: '4.17.21' },
    });
    fireEvent.click(screen.getByRole('button', { name: 'Create and start' }));
    await waitFor(() => {
      expect(posts()).toHaveLength(1);
    });
    expect(posts()[0]?.body).toMatchObject({
      title: 'Upgrade lodash',
      instructions: 'Upgrade lodash to 4.17.21 and run the tests.',
      profile: 'implementer',
      playbook: 'bug-fix',
      baseBranch: 'main',
      start: true,
    });
  });

  it('requires a title and shows the daemon’s field errors', async () => {
    render(<NewTaskForm onDone={vi.fn()} />, { snapshot });
    fireEvent.click(screen.getByRole('button', { name: 'Create' }));
    expect(await screen.findByText('A task needs a title.')).toBeInTheDocument();
    expect(posts()).toHaveLength(0);

    createResponse = {
      status: 400,
      body: {
        error: { code: 'unknown_profile', message: 'no agent profile named "x"', field: 'profile' },
      },
    };
    fireEvent.change(screen.getByRole('textbox', { name: 'Title' }), { target: { value: 'T' } });
    fireEvent.click(screen.getByRole('button', { name: 'Create' }));
    expect(await screen.findByText('no agent profile named "x"')).toBeInTheDocument();
  });

  it('offers only a tracker issue when the project cannot hold local tasks', async () => {
    setup(false);
    const onDone = vi.fn();
    render(<NewTaskForm onDone={onDone} />, { snapshot });
    await waitFor(() => {
      expect(screen.getByRole('radio', { name: 'Local issue (Itervox only)' })).toBeDisabled();
    });
    expect(screen.getByRole('radio', { name: 'Tracker issue' })).toBeChecked();
    fireEvent.change(screen.getByRole('textbox', { name: 'Title' }), { target: { value: 'T' } });
    fireEvent.click(screen.getByRole('button', { name: 'Create' }));
    await waitFor(() => {
      expect(onDone).toHaveBeenCalled();
    });
    expect(posts()[0]?.body).toMatchObject({ where: 'tracker' });
  });

  it('opens from ?newTask=1 (the Mac app) and the header button', async () => {
    function Host() {
      useNewTaskUrlOpen();
      return <NewTaskDialog />;
    }
    const { unmount } = render(<Host />, { snapshot, route: '/?newTask=1' });
    expect(await screen.findByRole('dialog', { name: 'New task' })).toBeInTheDocument();
    expect(useUIStore.getState().newTaskOpen).toBe(true);
    unmount();

    render(
      <>
        <DashboardIssuesHeader
          filteredCount={0}
          availableProfileCount={0}
          notificationsTotal={0}
          viewMode="board"
          setViewMode={vi.fn()}
          loading={false}
          onRefresh={vi.fn()}
          filterPills={[]}
          stateFilter="all"
          setStateFilter={vi.fn()}
          search=""
          setSearch={vi.fn()}
        />
        <NewTaskDialog />
      </>,
      { snapshot },
    );
    expect(screen.queryByRole('dialog', { name: 'New task' })).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: '+ New task' }));
    expect(await screen.findByRole('dialog', { name: 'New task' })).toBeInTheDocument();
  });

  it('hides the New task button from a viewer', () => {
    render(
      <DashboardIssuesHeader
        filteredCount={0}
        availableProfileCount={0}
        notificationsTotal={0}
        viewMode="board"
        setViewMode={vi.fn()}
        loading={false}
        onRefresh={vi.fn()}
        filterPills={[]}
        stateFilter="all"
        setStateFilter={vi.fn()}
        search=""
        setSearch={vi.fn()}
      />,
      { snapshot: makeSnapshot({ callerScope: 'viewer' }) },
    );
    expect(screen.queryByRole('button', { name: '+ New task' })).not.toBeInTheDocument();
  });
});

describe('newTaskModel (#251)', () => {
  it('lists and fills placeholders like the daemon', () => {
    expect(templateVars('Upgrade {{ package }}', 'to {{version}} {{package}}')).toEqual([
      'package',
      'version',
    ]);
    expect(fillVars('a {{x}} b {{ y }}', { x: '1' })).toBe('a 1 b {{ y }}');
    const { request, errors } = buildTaskRequest(
      { ...EMPTY_DRAFT, title: 'T', baseBranch: 'has space' },
      {},
      false,
      true,
    );
    expect(request).toBeUndefined();
    expect(errors.baseBranch).toBeDefined();
  });
});
