// Command palette sources and actions: what each offers, when, and what
// running it does. Also the per-viewer recents store.
import { afterEach, describe, expect, it, vi } from 'vitest';
import { actionCommands, type PaletteActionContext } from '../paletteActions';
import {
  automationCommands,
  helpCommands,
  logsCommand,
  memoryCommands,
  profileCommands,
  ruleCommands,
  sessionCommands,
  settingsCommands,
} from '../paletteSources';
import { loadRecents, recordRecent, RECENTS_CAP, RECENTS_KEY } from '../recents';
import type { AutomationDef, MemoryEntry, ProfileDef, SessionRow } from '../../../../types/schemas';
import type { SettingsFieldDef } from '../../../../lib/settingsSchema';

const none: PaletteActionContext = {
  newTask: null,
  dispatch: null,
  theme: null,
  projects: [],
  switchProject: () => undefined,
  analyzeDeps: null,
  refreshModels: null,
  pr: null,
  review: null,
};
const ids = (ctx: Partial<PaletteActionContext>) =>
  actionCommands({ ...none, ...ctx }).map((c) => c.id);
const fn = () => undefined;

describe('actionCommands availability', () => {
  it('offers nothing when nothing is available', () => {
    expect(ids({})).toEqual([]);
  });

  it.each<[string, Partial<PaletteActionContext>, string]>([
    ['new task', { newTask: fn }, 'action:new-task'],
    ['pause dispatch', { dispatch: { paused: false, set: fn } }, 'action:pause-dispatch'],
    ['resume dispatch', { dispatch: { paused: true, set: fn } }, 'action:resume-dispatch'],
    ['theme', { theme: { current: 'dark', toggle: fn } }, 'action:theme'],
    ['analyze deps', { analyzeDeps: fn }, 'action:analyze-deps'],
    ['refresh models', { refreshModels: fn }, 'action:refresh-models'],
    ['open PR', { pr: { identifier: 'WEB-1', open: fn } }, 'action:open-pr'],
    [
      'send review',
      { review: { identifier: 'WEB-1', comments: 2, send: fn } },
      'action:send-review',
    ],
  ])('%s appears only with its input', (_name, ctx, id) => {
    expect(ids(ctx)).toEqual([id]);
  });

  it('switch project lists each target', () => {
    expect(ids({ projects: ['api', 'docs'] })).toEqual([
      'action:project:api',
      'action:project:docs',
    ]);
  });

  it('the dispatch pause and the review confirm first; the theme names the other theme', () => {
    const set = vi.fn();
    const [pause] = actionCommands({ ...none, dispatch: { paused: false, set } });
    expect(pause.confirm?.confirmLabel).toBe('Pause dispatch');
    pause.run();
    expect(set).toHaveBeenCalledWith(true);
    const [resume] = actionCommands({ ...none, dispatch: { paused: true, set } });
    expect(resume.confirm?.tone).toBe('primary');
    resume.run();
    expect(set).toHaveBeenCalledWith(false);
    const [review] = actionCommands({
      ...none,
      review: { identifier: 'WEB-1', comments: 1, send: fn },
    });
    expect(review.label).toBe('Send review of WEB-1 (1 comment)');
    expect(review.confirm).toBeDefined();
    const [theme] = actionCommands({ ...none, theme: { current: 'light', toggle: fn } });
    expect(theme.label).toBe('Switch to dark theme');
  });

  it('switching project calls back with the name', () => {
    const switchProject = vi.fn();
    actionCommands({ ...none, projects: ['api'], switchProject })[0].run();
    expect(switchProject).toHaveBeenCalledWith('api');
  });
});

describe('sources', () => {
  const go = vi.fn();
  afterEach(() => {
    go.mockReset();
  });

  it('profiles: open for everyone, edit only when allowed', () => {
    const defs = { coder: { command: 'claude', backend: 'claude' } as ProfileDef };
    expect(profileCommands(defs, false, go).map((c) => c.id)).toEqual(['profile:coder']);
    const both = profileCommands(defs, true, go);
    expect(both.map((c) => c.id)).toEqual(['profile:coder', 'profile-edit:coder']);
    both[0].run();
    expect(go).toHaveBeenLastCalledWith('/agents#profile-coder');
    both[1].run();
    expect(go).toHaveBeenLastCalledWith('/agents?editProfile=coder');
  });

  it('automations open their editor', () => {
    const a = {
      id: 'nightly qa',
      enabled: false,
      profile: 'qa',
      trigger: { type: 'cron' },
    } as AutomationDef;
    const [c] = automationCommands([a], go);
    expect(c.detail).toBe('cron · qa · off');
    c.run();
    expect(go).toHaveBeenCalledWith('/automations?openAutomation=nightly%20qa');
  });

  it('rules open their layer; unknown layers are skipped', () => {
    const open = vi.fn();
    const rows = ruleCommands(
      [
        { name: 'project', rules: [{ id: 'pnpm', severity: 'must', text: 'Use pnpm.' }] },
        { name: 'workflow', rules: [{ id: 'x', severity: 'should', text: 'X' }] },
      ],
      open,
    );
    expect(rows.map((c) => c.id)).toEqual(['rule:project:pnpm']);
    rows[0].run();
    expect(open).toHaveBeenCalledWith('project');
  });

  it('memory entries open by id, long text is shortened', () => {
    const open = vi.fn();
    const [c] = memoryCommands(
      [{ id: 'm-1', type: 'fact', text: 'x'.repeat(200), tags: ['a'], profiles: [] } as never],
      open,
    );
    expect(c.label.length).toBeLessThan(100);
    expect(c.keywords).toBe('a');
    c.run();
    expect(open).toHaveBeenCalledWith('m-1');
    expect(memoryCommands([] as MemoryEntry[], open)).toEqual([]);
  });

  it('settings fields jump to /settings?setting=<key>', () => {
    const f = {
      key: 'agent.max_turns',
      section: 'agent',
      label: 'Max turns',
      group: 'Limits',
    } as SettingsFieldDef;
    const [c] = settingsCommands([f], go);
    expect(c.keywords).toContain('agent.max_turns');
    c.run();
    expect(go).toHaveBeenCalledWith('/settings?setting=agent.max_turns');
  });

  it('sessions open by key; rows without one are skipped', () => {
    const rows = sessionCommands(
      [
        { id: 's-1', owner: 'itervox', identifier: 'WEB-1', status: 'input_required' },
        { id: '', owner: 'external', identifier: '', status: 'running' },
      ] as SessionRow[],
      (s) => s.id,
      (s) => s.identifier,
      go,
    );
    expect(rows).toHaveLength(1);
    expect(rows[0].detail).toBe('input required');
    rows[0].run();
    expect(go).toHaveBeenCalledWith('/sessions?id=s-1');
  });

  it('search logs: none without text or target; keeps the text when Logs would match it', () => {
    const setSearch = vi.fn();
    expect(logsCommand('', { identifier: 'WEB-1', title: 'Login' }, go, setSearch)).toEqual([]);
    expect(logsCommand('login', null, go, setSearch)).toEqual([]);
    logsCommand('login', { identifier: 'WEB-1', title: 'Fix login' }, go, setSearch)[0].run();
    expect(setSearch).toHaveBeenLastCalledWith('login');
    expect(go).toHaveBeenLastCalledWith('/logs/WEB-1');
    logsCommand('wb1', { identifier: 'WEB-1', title: 'Fix login' }, go, setSearch)[0].run();
    expect(setSearch).toHaveBeenLastCalledWith('WEB-1');
  });

  it('help rows prefill their prefix', () => {
    expect(helpCommands().map((c) => c.prefill)).toEqual(['>', '#', '@', '/', '?']);
  });
});

describe('recents', () => {
  afterEach(() => {
    window.localStorage.clear();
    vi.restoreAllMocks();
  });

  it('records newest first, once, capped', () => {
    for (let i = 0; i < RECENTS_CAP + 3; i++) recordRecent(`c${String(i)}`);
    recordRecent('c5');
    const list = loadRecents();
    expect(list).toHaveLength(RECENTS_CAP);
    expect(list[0]).toBe('c5');
    expect(list.filter((x) => x === 'c5')).toHaveLength(1);
  });

  it('ignores junk and survives storage that throws', () => {
    window.localStorage.setItem(RECENTS_KEY, '{"not":"a list"}');
    expect(loadRecents()).toEqual([]);
    window.localStorage.setItem(RECENTS_KEY, 'not json');
    expect(loadRecents()).toEqual([]);
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => {
      throw new Error('blocked');
    });
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new Error('blocked');
    });
    expect(loadRecents()).toEqual([]);
    expect(recordRecent('x')).toEqual(['x']);
  });
});
