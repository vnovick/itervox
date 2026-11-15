import { beforeEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { render } from '../../../test/render';
import { useItervoxStore } from '../../../store/itervoxStore';
import { useToastStore } from '../../../store/toastStore';
import { useUIStore } from '../../../store/uiStore';
import { SchemaSettingsCard } from '../schema/SchemaSettingsCard';

vi.mock('../../../auth/authedFetch', () => ({ authedFetch: vi.fn() }));
import { authedFetch } from '../../../auth/authedFetch';

const SCHEMA_URL = 'https://itervox.dev/schema/settings.schema.json';

// A schema as the daemon serves it. `brand_new_knob` stands for a setting
// added to the Go registry: nothing on the page names it.
const SCHEMA = {
  $id: SCHEMA_URL,
  properties: {
    $schema: { type: 'string' },
    agent: {
      type: 'object',
      properties: {
        max_concurrent_agents: {
          type: 'integer',
          title: 'Max concurrent agents',
          description: 'Most agent workers running at once.',
          default: 10,
          minimum: 1,
          'x-itervox-group': 'Capacity',
          'x-itervox-live': true,
        },
        brand_new_knob: {
          type: 'integer',
          title: 'Brand new knob',
          description: 'A setting this page has no code for.',
          default: 3,
          minimum: 1,
          'x-itervox-group': 'Experimental',
        },
        inline_input: {
          type: 'boolean',
          title: 'Inline input',
          default: false,
          'x-itervox-group': 'Core',
          'x-itervox-live': true,
        },
        dispatch_strategy: {
          type: 'string',
          title: 'Dispatch strategy',
          enum: ['round-robin', 'least-loaded'],
          'x-itervox-group': 'SSH',
        },
        ssh_hosts: {
          type: 'array',
          title: 'SSH hosts',
          items: { type: 'string' },
          'x-itervox-group': 'SSH',
        },
        profiles: {
          type: 'object',
          title: 'Agent profiles',
          'x-itervox-group': 'Profiles',
          'x-itervox-editor': 'profiles',
        },
        budget: {
          type: 'object',
          properties: {
            tokens: {
              type: 'integer',
              title: 'Per-issue token budget',
              'x-itervox-group': 'Budgets',
            },
          },
        },
      },
    },
    polling: {
      type: 'object',
      properties: {
        interval_ms: { type: 'integer', title: 'Poll interval (ms)', 'x-itervox-group': 'Polling' },
      },
    },
  },
};

const PROJECT_RAW = `// Team settings — keep this comment.
{
  "agent": {"max_concurrent_agents": 3},
}
`;

function layersView() {
  return {
    schemaUrl: SCHEMA_URL,
    layers: [
      {
        name: 'user',
        path: '/home/me/.itervox/settings.jsonc',
        writable: true,
        exists: false,
        raw: '',
        version: '',
        values: {},
        unknown: [],
      },
      {
        name: 'project',
        path: '/repo/.itervox/settings.jsonc',
        writable: true,
        exists: true,
        raw: PROJECT_RAW,
        version: 'p1',
        values: { 'agent.max_concurrent_agents': 3 },
        unknown: ['agent.max_turnz'],
      },
      {
        name: 'local',
        path: '/repo/.itervox/settings.local.jsonc',
        writable: true,
        exists: false,
        raw: '',
        version: '',
        values: {},
        unknown: [],
      },
    ],
    effective: [
      {
        key: 'agent.max_concurrent_agents',
        value: 3,
        layer: 'project',
        path: '/repo/.itervox/settings.jsonc',
      },
      { key: 'agent.brand_new_knob', value: 3, layer: 'default' },
      { key: 'agent.ssh_hosts', value: ['a', 'b'], layer: 'workflow' },
    ],
  };
}

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

function urlOf(input: RequestInfo | URL): string {
  if (typeof input === 'string') return input;
  return input instanceof URL ? input.href : input.url;
}

function serve() {
  vi.mocked(authedFetch).mockImplementation((input: RequestInfo | URL, init?: RequestInit) => {
    const url = urlOf(input);
    if (init?.method === 'PUT') {
      const layer = url.split('/').pop() ?? '';
      return Promise.resolve(
        json({
          ...layersView(),
          layer,
          applied: [],
          restartRequired: ['agent.brand_new_knob'],
          shadowed: {},
        }),
      );
    }
    if (url === '/api/v1/settings/schema') return Promise.resolve(json(SCHEMA));
    if (url === '/api/v1/settings/layers') return Promise.resolve(json(layersView()));
    return Promise.resolve(json({}, 404));
  });
}

function put(): { url: string; body: Record<string, unknown> } {
  const call = vi.mocked(authedFetch).mock.calls.find(([, init]) => init?.method === 'PUT');
  if (!call) throw new Error('no PUT');
  const body = call[1]?.body;
  if (typeof body !== 'string') throw new Error('expected a string body');
  return { url: urlOf(call[0]), body: JSON.parse(body) as Record<string, unknown> };
}

describe('schema-driven Settings section (#158)', () => {
  const refreshSnapshot = vi.fn(() => Promise.resolve());

  beforeEach(() => {
    vi.clearAllMocks();
    serve();
  });

  function renderCard() {
    const ui = render(<SchemaSettingsCard />);
    useItervoxStore.setState({ refreshSnapshot });
    useToastStore.setState({ toasts: [] });
    return ui;
  }

  it('renders a new layered setting from the schema with no form code', async () => {
    renderCard();
    const knob = await screen.findByLabelText('Brand new knob');
    expect(knob).toHaveValue(3);
    const field = screen.getByTestId('setting-agent.brand_new_knob');
    expect(field).toHaveTextContent('agent.brand_new_knob');
    expect(field).toHaveTextContent('from Default');
    expect(field).toHaveTextContent('A setting this page has no code for.');
    expect(screen.getByText('Experimental')).toBeInTheDocument();
    // Every type gets its generic renderer.
    expect(screen.getByLabelText('Inline input')).toHaveAttribute('type', 'checkbox');
    expect(screen.getByLabelText('Dispatch strategy').tagName).toBe('SELECT');
    expect(screen.getByLabelText('SSH hosts')).toHaveValue('a\nb');
    expect(screen.getByTestId('setting-agent.profiles')).toHaveTextContent(
      'Edited on the profiles page.',
    );
    expect(screen.getByLabelText('Per-issue token budget')).toBeInTheDocument();
    expect(screen.getByTestId('setting-agent.max_concurrent_agents')).toHaveTextContent(
      'set in this layer',
    );
    expect(screen.getByRole('status')).toHaveTextContent('Itervox does not read: agent.max_turnz');
  });

  it('a save writes the chosen layer: the local tab saves to the local file', async () => {
    const user = userEvent.setup();
    renderCard();
    await screen.findByLabelText('Brand new knob');
    await user.click(screen.getByRole('tab', { name: 'Project local' }));
    expect(useUIStore.getState().settingsLayer).toBe('local');
    const panel = screen.getByTestId('settings-layer-local');
    expect(within(panel).getByText('/repo/.itervox/settings.local.jsonc')).toBeInTheDocument();

    const knob = within(panel).getByLabelText('Brand new knob');
    await user.clear(knob);
    await user.type(knob, '7');
    await user.click(within(panel).getByLabelText('Inline input'));
    await user.click(within(panel).getByRole('button', { name: 'Save to Project local (2)' }));

    await waitFor(() => {
      expect(put()).toEqual({
        url: '/api/v1/settings/layers/local',
        body: {
          baseVersion: '',
          values: { 'agent.brand_new_knob': 7, 'agent.inline_input': true },
        },
      });
    });
    await waitFor(() => {
      expect(refreshSnapshot).toHaveBeenCalled();
    });
    expect(useToastStore.getState().toasts.map((t) => t.message)).toContain(
      'Saved to the local settings; applies after a restart: agent.brand_new_knob',
    );
  });

  it('unsets a key the layer sets, and blocks a save with an invalid value', async () => {
    const user = userEvent.setup();
    renderCard();
    const workers = await screen.findByLabelText('Max concurrent agents');
    await user.clear(workers);
    await user.type(workers, '0');
    expect(screen.getByRole('alert')).toHaveTextContent('must be at least 1');
    expect(screen.getByRole('button', { name: /Save to Project/ })).toBeDisabled();

    const field = screen.getByTestId('setting-agent.max_concurrent_agents');
    await user.click(within(field).getByRole('button', { name: 'Unset here' }));
    expect(screen.queryByRole('alert')).not.toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Save to Project (1)' }));
    await waitFor(() => {
      expect(put()).toEqual({
        url: '/api/v1/settings/layers/project',
        body: { baseVersion: 'p1', values: { 'agent.max_concurrent_agents': null } },
      });
    });
  });

  it('JSON view shows the file with its comments, validates it, and saves the text', async () => {
    const user = userEvent.setup();
    renderCard();
    await screen.findByLabelText('Brand new knob');
    await user.click(screen.getByRole('button', { name: 'JSON' }));
    const area = screen.getByLabelText('Settings file (JSONC)');
    expect((area as HTMLTextAreaElement).value).toContain('// Team settings — keep this comment.');

    await user.clear(area);
    await user.click(area);
    await user.paste('{"tracker": {}}');
    expect(screen.getByTestId('settings-json-status')).toHaveTextContent('cannot be set here');
    expect(screen.getByRole('button', { name: 'Save file to Project' })).toBeDisabled();

    await user.clear(area);
    await user.click(area);
    await user.paste('// mine\n{"agent": {"brand_new_knob": 5}}');
    expect(screen.getByTestId('settings-json-status')).toHaveTextContent('Valid: 1 setting.');
    await user.click(screen.getByRole('button', { name: 'Save file to Project' }));
    await waitFor(() => {
      expect(put()).toEqual({
        url: '/api/v1/settings/layers/project',
        body: { baseVersion: 'p1', raw: '// mine\n{"agent": {"brand_new_knob": 5}}' },
      });
    });
  });

  it('filters the fields and reloads after a conflicting save', async () => {
    const user = userEvent.setup();
    renderCard();
    await screen.findByLabelText('Brand new knob');
    await user.type(screen.getByLabelText('Filter settings'), 'poll');
    expect(screen.queryByLabelText('Brand new knob')).not.toBeInTheDocument();
    expect(screen.getByLabelText('Poll interval (ms)')).toBeInTheDocument();
    await user.clear(screen.getByLabelText('Filter settings'));
    await user.type(screen.getByLabelText('Filter settings'), 'zzz');
    expect(screen.getByText('No setting matches.')).toBeInTheDocument();

    vi.mocked(authedFetch).mockImplementation((input: RequestInfo | URL, init?: RequestInit) => {
      if (init?.method === 'PUT')
        return Promise.resolve(json({ error: { code: 'settings_stale', message: 'stale' } }, 409));
      const url = urlOf(input);
      return Promise.resolve(json(url.endsWith('/schema') ? SCHEMA : layersView()));
    });
    await user.clear(screen.getByLabelText('Filter settings'));
    const poll = screen.getByLabelText('Poll interval (ms)');
    await user.type(poll, '5000');
    await user.click(screen.getByRole('button', { name: 'Save to Project (1)' }));
    await waitFor(() => {
      expect(
        useToastStore
          .getState()
          .toasts.map((t) => t.message)
          .join(),
      ).toContain('changed since it was loaded');
    });
    expect(refreshSnapshot).not.toHaveBeenCalled();
  });
});
