import { beforeEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { render } from '../../../test/render';
import { useToastStore } from '../../../store/toastStore';
import { useUIStore } from '../../../store/uiStore';
import Rules from '../index';

vi.mock('../../../components/common/PageMeta', () => ({ default: () => null }));
vi.mock('../../../auth/authedFetch', () => ({ authedFetch: vi.fn() }));
import { authedFetch } from '../../../auth/authedFetch';

const SCHEMA = 'https://itervox.dev/schema/rules.schema.json';
const PROJECT_RAW = `// Team rules — keep this comment.
{
  "rules": [
    // Lockfile is pnpm.
    {"id": "pnpm", "severity": "must", "text": "Use pnpm."},
  ],
}
`;

function layers(projectVersion = 'v1') {
  return {
    schemaUrl: SCHEMA,
    layers: [
      {
        name: 'user',
        path: '/home/me/.itervox/rules.json',
        writable: true,
        exists: false,
        raw: '',
        version: '',
        rules: [],
      },
      {
        name: 'project',
        path: '/repo/.itervox/rules.json',
        writable: true,
        exists: true,
        raw: PROJECT_RAW,
        version: projectVersion,
        rules: [{ id: 'pnpm', severity: 'must', text: 'Use pnpm.' }],
      },
      {
        name: 'local',
        path: '/repo/.itervox/rules.local.json',
        writable: true,
        exists: true,
        raw: '{"rules": [{"id": 1}]}',
        version: 'v9',
        rules: [],
        error: 'rules: /repo/.itervox/rules.local.json: rules[0].id: must be a string',
      },
    ],
  };
}

const PREVIEW = {
  profile: 'default',
  backend: 'claude',
  projects: ['repo'],
  rules: [{ id: 'pnpm', severity: 'must', text: 'Use pnpm.', layer: 'user', overrides: undefined }],
  conflicts: [
    {
      id: 'pnpm',
      layer: 'project',
      keptLayer: 'user',
      message: 'the project rule "pnpm" was ignored',
    },
  ],
  errors: [],
  block: '## Rules\n\n### Must\n\n- Use pnpm. (pnpm)',
  hash: 'sha256:abc',
  ids: ['pnpm'],
  templatePlacesRules: false,
};

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

type Handler = (url: string, init?: RequestInit) => Response;

function urlOf(input: RequestInfo | URL): string {
  if (typeof input === 'string') return input;
  return input instanceof URL ? input.href : input.url;
}

function serve(put: Handler) {
  vi.mocked(authedFetch).mockImplementation((input: RequestInfo | URL, init?: RequestInit) => {
    const url = urlOf(input);
    if (init?.method === 'PUT') return Promise.resolve(put(url, init));
    if (url.startsWith('/api/v1/rules/preview')) return Promise.resolve(json(PREVIEW));
    if (url === '/api/v1/rules') return Promise.resolve(json(layers()));
    return Promise.resolve(json({}, 404));
  });
}

function putBody(): { baseVersion: string; raw?: string; rules?: unknown[] } {
  const call = vi.mocked(authedFetch).mock.calls.find(([, init]) => init?.method === 'PUT');
  if (!call) throw new Error('no PUT');
  const body = call[1]?.body;
  if (typeof body !== 'string') throw new Error('expected a string body');
  return JSON.parse(body) as { baseVersion: string; raw?: string; rules?: unknown[] };
}

describe('Rules editor (#157)', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    useToastStore.setState({ toasts: [] });
  });

  it('has a tab per writable layer and edits the project layer in the form view', async () => {
    serve((url) => {
      expect(url).toBe('/api/v1/rules/project');
      return json({ ...layers('v2').layers[1], version: 'v2' });
    });
    const user = userEvent.setup();
    render(<Rules />);

    const tabs = await screen.findAllByRole('tab');
    expect(tabs.map((t) => t.textContent)).toEqual(['User', 'Project', 'Project local']);
    const panel = await screen.findByTestId('rules-layer-project');
    expect(within(panel).getByText('/repo/.itervox/rules.json')).toBeInTheDocument();

    const text = within(panel).getByLabelText('Text');
    await user.clear(text);
    await user.type(text, 'Use pnpm 9.');
    await user.click(within(panel).getByRole('button', { name: 'Add rule' }));
    const ids = within(panel).getAllByLabelText('ID');
    await user.type(ids[1], 'small-prs');
    await user.type(within(panel).getAllByLabelText('Text')[1], 'Keep PRs small.');
    await user.type(within(panel).getAllByLabelText('Profiles')[1], 'implementer, reviewer');
    await user.click(within(panel).getByRole('button', { name: 'Save rules' }));

    await waitFor(() => {
      expect(putBody()).toEqual({
        baseVersion: 'v1',
        rules: [
          { id: 'pnpm', severity: 'must', text: 'Use pnpm 9.' },
          {
            id: 'small-prs',
            severity: 'should',
            text: 'Keep PRs small.',
            scope: { profiles: ['implementer', 'reviewer'] },
          },
        ],
      });
    });
    await waitFor(() => {
      expect(useToastStore.getState().toasts.map((t) => t.message)).toContain(
        'Saved the project rules',
      );
    });
  });

  it('blocks saving a form with an invalid or duplicate rule', async () => {
    serve(() => json({}, 500));
    const user = userEvent.setup();
    render(<Rules />);
    const panel = await screen.findByTestId('rules-layer-project');
    await user.click(within(panel).getByRole('button', { name: 'Add rule' }));
    await user.type(within(panel).getAllByLabelText('ID')[1], 'PNPM');
    await user.type(within(panel).getAllByLabelText('Text')[1], 'Again.');
    expect(within(panel).getByRole('alert')).toHaveTextContent('already used by rule 1');
    expect(within(panel).getByRole('button', { name: 'Save rules' })).toBeDisabled();
  });

  it('JSON view shows the file with its comments, validates it, and saves the text as typed', async () => {
    serve(() => json({ ...layers().layers[1], version: 'v2' }));
    const user = userEvent.setup();
    render(<Rules />);
    const panel = await screen.findByTestId('rules-layer-project');
    await user.click(within(panel).getByRole('button', { name: 'JSON' }));
    expect(useUIStore.getState().rulesView).toBe('json');
    const area = within(panel).getByLabelText('Rules file (JSONC)');
    expect((area as HTMLTextAreaElement).value).toContain('// Lockfile is pnpm.');

    await user.clear(area);
    await user.click(area);
    await user.paste('{"rules": [{"id": "a"}]}');
    expect(within(panel).getByTestId('rules-json-status')).toHaveTextContent('rules.0.severity');
    expect(within(panel).getByRole('button', { name: 'Save file' })).toBeDisabled();

    await user.clear(area);
    await user.click(area);
    await user.paste('// mine\n{"rules": [{"id": "a", "severity": "should", "text": "A."}]}');
    expect(within(panel).getByTestId('rules-json-status')).toHaveTextContent('Valid: 1 rule.');
    await user.click(within(panel).getByRole('button', { name: 'Save file' }));
    await waitFor(() => {
      expect(putBody()).toEqual({
        baseVersion: 'v1',
        raw: '// mine\n{"rules": [{"id": "a", "severity": "should", "text": "A."}]}',
      });
    });
  });

  it('shows a broken layer and keeps its form closed until it is fixed', async () => {
    serve(() => json({}, 500));
    const user = userEvent.setup();
    render(<Rules />);
    await user.click(await screen.findByRole('tab', { name: 'Project local' }));
    const panel = await screen.findByTestId('rules-layer-local');
    expect(within(panel).getByRole('alert')).toHaveTextContent('must be a string');
    expect(within(panel).getByText(/Fix the file in the JSON view/)).toBeInTheDocument();
  });

  it('reloads and says so when the file changed since it was loaded (409)', async () => {
    serve(() => json({ error: { code: 'rules_stale', message: 'stale' } }, 409));
    const user = userEvent.setup();
    render(<Rules />);
    const panel = await screen.findByTestId('rules-layer-project');
    await user.click(within(panel).getByRole('button', { name: 'Save rules' }));
    await waitFor(() => {
      expect(
        useToastStore
          .getState()
          .toasts.map((t) => t.message)
          .join(' '),
      ).toContain('changed since it was loaded');
    });
  });

  it('previews the effective rules with conflicts for a profile and an issue', async () => {
    serve(() => json({}, 500));
    const user = userEvent.setup();
    render(<Rules />);
    const preview = await screen.findByTestId('rules-preview');
    expect(within(preview).getByText('Use pnpm.')).toBeInTheDocument();
    expect(within(preview).getByRole('alert')).toHaveTextContent(
      'the project rule "pnpm" was ignored',
    );
    expect(within(preview).getByText('sha256:abc')).toBeInTheDocument();

    await user.type(screen.getByLabelText('Issue'), 'ENG-7{Enter}');
    await waitFor(() => {
      expect(
        vi
          .mocked(authedFetch)
          .mock.calls.some(([u]) => urlOf(u) === '/api/v1/rules/preview?issue=ENG-7'),
      ).toBe(true);
    });
  });
});
