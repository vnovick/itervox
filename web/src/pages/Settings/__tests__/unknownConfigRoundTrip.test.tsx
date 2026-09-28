import type { ReactNode } from 'react';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { AutomationsCard } from '../AutomationsCard';
import { ProfilesCard } from '../ProfilesCard';
import { useSettingsActions } from '../../../hooks/useSettingsActions';
import { useItervoxStore } from '../../../store/itervoxStore';
import { StateSnapshotSchema, type StateSnapshot } from '../../../types/schemas';

// CORE-047 round 2 — a stale dashboard bundle talking to a newer daemon must
// never rewrite config it does not understand. An unknown automation trigger
// (and its trigger/policy fields), filter matchMode, switchToBackend, and an
// unknown profile allowed action are preserved raw and sent back unchanged
// when the operator saves without touching them.

const RAW_SNAPSHOT = {
  generatedAt: 'x',
  counts: { running: 0, retrying: 0, paused: 0 },
  running: [],
  retrying: [],
  paused: [],
  maxConcurrentAgents: 1,
  maxRetries: 1,
  maxSwitchesPerIssuePerWindow: 1,
  switchWindowHours: 1,
  rateLimits: null,
  availableProfiles: ['qa'],
  profileDefs: {
    qa: {
      command: 'claude',
      backend: 'claude',
      enabled: true,
      allowedActions: ['teleport', 'comment'],
      futureProfileKnob: { level: 3 },
    },
  },
  automations: [
    {
      id: 'future-rule',
      enabled: true,
      profile: 'qa',
      trigger: { type: 'on_full_moon', state: 'Waxing', cron: '0 0 * * *' },
      filter: { matchMode: 'most', states: ['Todo'] },
      policy: { switchToBackend: 'gemini', moveToState: 'Howling', autoResume: true },
    },
    {
      id: 'known-rule',
      enabled: true,
      profile: 'qa',
      futureTopLevel: true,
      trigger: { type: 'cron', cron: '0 9 * * 1', futureTriggerKnob: 'x' },
      filter: { states: ['Todo'], futureFilterKnob: [1, 2] },
      policy: { autoResume: true, futurePolicyKnob: 42 },
    },
    {
      id: 'rl-rule',
      enabled: true,
      profile: 'qa',
      trigger: { type: 'rate_limited' },
      policy: { autoResume: true, switchToProfile: 'qa', switchToBackend: 'gemini' },
    },
  ],
};

type FetchFn = (input: string, init?: RequestInit) => Promise<Response>;
let fetchMock: ReturnType<typeof vi.fn<FetchFn>>;
let snapshot: StateSnapshot;

function withQueryClient(children: ReactNode) {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return <QueryClientProvider client={qc}>{children}</QueryClientProvider>;
}

function writes(path: string): Record<string, unknown>[] {
  return fetchMock.mock.calls
    .filter(([url, init]) => url === path && init?.body !== undefined)
    .map(([, init]) => JSON.parse(init?.body as string) as Record<string, unknown>);
}

function Automations() {
  const actions = useSettingsActions();
  return withQueryClient(
    <AutomationsCard
      automations={snapshot.automations ?? []}
      availableProfiles={['qa']}
      availableStates={['Todo']}
      availableLabels={[]}
      onSave={actions.setAutomations}
      onSaveTyped={actions.setAutomationsTyped}
    />,
  );
}

function Profiles() {
  const actions = useSettingsActions();
  return withQueryClient(
    <ProfilesCard
      profileDefs={snapshot.profileDefs ?? {}}
      onUpsert={actions.upsertProfile}
      onDelete={actions.deleteProfile}
    />,
  );
}

beforeEach(() => {
  snapshot = StateSnapshotSchema.parse(RAW_SNAPSHOT);
  useItervoxStore.setState({ snapshot, refreshSnapshot: vi.fn().mockResolvedValue(undefined) });
  fetchMock = vi
    .fn<FetchFn>()
    .mockImplementation(() => Promise.resolve(new Response('{}', { status: 200 })));
  global.fetch = fetchMock as unknown as typeof fetch;
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe('unknown config round-trip', () => {
  it('shows an unknown trigger read-only and saves the automation form with the original values', async () => {
    const user = userEvent.setup();
    render(<Automations />);
    expect(screen.getAllByText(/Unknown trigger 'on_full_moon'/).length).toBeGreaterThan(0);

    await user.click(screen.getAllByRole('button', { name: /edit/i })[0]);
    expect(screen.getAllByText(/Unknown trigger 'on_full_moon'/).length).toBeGreaterThan(0);
    await user.click(screen.getByRole('button', { name: /Save Changes/i }));

    await waitFor(() => {
      expect(writes('/api/v1/settings/automations')).toHaveLength(1);
    });
    const { automations } = writes('/api/v1/settings/automations')[0] as {
      automations: Record<string, unknown>[];
    };
    expect(automations[0]).toMatchObject({
      id: 'future-rule',
      trigger: { type: 'on_full_moon', state: 'Waxing', cron: '0 0 * * *' },
      filter: { matchMode: 'most', states: ['Todo'] },
      policy: { switchToBackend: 'gemini', moveToState: 'Howling', autoResume: true },
    });
    expect(automations[0]).not.toHaveProperty('unknownFields');
    // The untouched sibling rule keeps its unknown switchToBackend too.
    expect(automations[2]).toMatchObject({
      trigger: { type: 'rate_limited' },
      policy: { switchToProfile: 'qa', switchToBackend: 'gemini' },
    });
  }, 15000);

  it('keeps unknown values when a different rule is deleted', async () => {
    const user = userEvent.setup();
    render(<Automations />);
    const rows = screen.getAllByRole('button', { name: /^delete$/i });
    await user.click(rows[1]);
    await waitFor(() => {
      expect(writes('/api/v1/settings/automations')).toHaveLength(1);
    });
    const { automations } = writes('/api/v1/settings/automations')[0] as {
      automations: Record<string, unknown>[];
    };
    expect(automations).toHaveLength(2);
    expect(automations[0]).toMatchObject({ trigger: { type: 'on_full_moon' } });
  }, 15000);

  it('keeps unknown keys under a known trigger through an untouched form save', async () => {
    const user = userEvent.setup();
    render(<Automations />);
    await user.click(screen.getAllByRole('button', { name: /edit/i })[1]);
    await user.click(screen.getByRole('button', { name: /Save Changes/i }));
    await waitFor(() => {
      expect(writes('/api/v1/settings/automations')).toHaveLength(1);
    });
    const { automations } = writes('/api/v1/settings/automations')[0] as {
      automations: Record<string, unknown>[];
    };
    expect(automations[1]).toEqual({
      id: 'known-rule',
      enabled: true,
      profile: 'qa',
      futureTopLevel: true,
      trigger: { type: 'cron', cron: '0 9 * * 1', futureTriggerKnob: 'x' },
      filter: { states: ['Todo'], futureFilterKnob: [1, 2] },
      policy: { autoResume: true, futurePolicyKnob: 42 },
    });
  }, 15000);

  it('saves the profile form untouched with the unknown allowed action still present', async () => {
    const user = userEvent.setup();
    render(<Profiles />);
    const card = screen.getByText('qa').closest('article') as HTMLElement;
    expect(within(card).getByText(/teleport/)).toBeInTheDocument();
    await user.click(within(card).getByRole('button', { name: 'Edit' }));
    await user.click(screen.getByRole('button', { name: 'Save Changes' }));
    await waitFor(() => {
      expect(writes('/api/v1/settings/profiles/qa')).toHaveLength(1);
    });
    const body = writes('/api/v1/settings/profiles/qa')[0] as {
      allowedActions: string[];
      futureProfileKnob?: unknown;
    };
    expect([...body.allowedActions].sort()).toEqual(['comment', 'teleport']);
    expect(body.futureProfileKnob).toEqual({ level: 3 });
  }, 15000);

  it('keeps the unknown allowed action when the profile is only toggled', async () => {
    const user = userEvent.setup();
    render(<Profiles />);
    await user.click(screen.getByRole('button', { name: 'Deactivate' }));
    await waitFor(() => {
      expect(writes('/api/v1/settings/profiles/qa')).toHaveLength(1);
    });
    const body = writes('/api/v1/settings/profiles/qa')[0] as {
      allowedActions: string[];
      enabled: boolean;
    };
    expect(body.enabled).toBe(false);
    expect([...body.allowedActions].sort()).toEqual(['comment', 'teleport']);
  }, 15000);
});
