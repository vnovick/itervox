// Command palette ranking: the fuzzy matcher, scope prefixes, recents and
// the ranked/grouped order.
import { describe, expect, it } from 'vitest';
import { fuzzyScore, normalizeId } from '../fuzzy';
import { inScope, parseQuery, rankCommands, scoreCommand, type Command } from '../paletteModel';

const cmd = (over: Partial<Command> & Pick<Command, 'id' | 'label'>): Command => ({
  group: 'actions',
  run: () => undefined,
  ...over,
});

describe('fuzzyScore', () => {
  it.each([
    // needle, haystack, matches?
    ['wb207', 'WEB-207', true],
    ['web207', 'WEB-207', true],
    ['pd', 'Pause dispatch', true],
    ['set', 'Settings', true],
    ['', 'anything', true],
    ['xyz', 'Pause dispatch', false],
    ['dispatchx', 'dispatch', false],
    ['tool', 'Tidy the demo fixtures', false], // too scattered: scores ≤ 0
    ['ab', 'a', false],
  ])('%j in %j → %s', (needle, haystack, ok) => {
    expect(fuzzyScore(needle, haystack) !== null).toBe(ok);
  });

  it.each([
    // the first haystack must outscore the second for the needle
    ['set', 'Settings', 'Reset them'], // prefix beats mid-word
    ['pd', 'Pause dispatch', 'Updated'], // word starts beat a run inside a word
    ['log', 'Go to Logs', 'Go to Timeline overlong'], // consecutive beats scattered
    ['ab', 'ab', 'abc'], // exact beats prefix
    ['tl', 'Go to Logs', 'Go battle'], // word starts beat a consecutive run mid-word
    ['mca', 'maxConcurrentAgents', 'macaroni'], // camelCase word starts
  ])('%j prefers %j over %j', (needle, better, worse) => {
    const a = fuzzyScore(needle, better);
    const b = fuzzyScore(needle, worse);
    expect(a).not.toBeNull();
    expect(a ?? 0).toBeGreaterThan(b ?? Number.NEGATIVE_INFINITY);
  });

  it('normalizeId drops separators and case', () => {
    expect(normalizeId('WEB-207')).toBe('web207');
    expect(normalizeId('owner/repo#12')).toBe('ownerrepo12');
  });
});

describe('parseQuery and scopes', () => {
  it.each([
    ['', 'all', ''],
    ['  pause ', 'all', 'pause'],
    ['>pause', 'actions', 'pause'],
    ['> pause', 'actions', 'pause'],
    ['#WEB-2', 'issues', 'WEB-2'],
    ['@coder', 'profiles', 'coder'],
    ['/logs', 'pages', 'logs'],
    ['?', 'help', ''],
  ])('%j → scope %s, text %j', (raw, scope, text) => {
    expect(parseQuery(raw)).toEqual({ scope, text });
  });

  it('maps groups to scopes', () => {
    expect(inScope('runs', 'actions')).toBe(true);
    expect(inScope('issues', 'actions')).toBe(false);
    expect(inScope('logs', 'issues')).toBe(true);
    expect(inScope('profiles', 'profiles')).toBe(true);
    expect(inScope('settings', 'pages')).toBe(false);
    expect(inScope('help', 'all')).toBe(false);
    expect(inScope('help', 'help')).toBe(true);
  });
});

describe('scoreCommand', () => {
  it('needs every word to match the label, tag or keywords', () => {
    const c = cmd({ id: 'i', label: 'Open WEB-207 — Fix login', tag: 'WEB-207', keywords: 'Todo' });
    expect(scoreCommand('open login', c)).not.toBeNull();
    expect(scoreCommand('todo', c)).not.toBeNull();
    expect(scoreCommand('open nothing', c)).toBeNull();
  });

  it('boosts an exact or prefix id match', () => {
    const tagged = cmd({ id: 'a', label: 'Open it', tag: 'WEB-207' });
    const untagged = cmd({ id: 'b', label: 'web 207' });
    expect(scoreCommand('web-207', tagged) ?? 0).toBeGreaterThan(
      scoreCommand('web-207', untagged) ?? 0,
    );
    expect(scoreCommand('web2', tagged) ?? 0).toBeGreaterThan(scoreCommand('web2', untagged) ?? 0);
    // An exact id clearly beats a longer id it is a prefix of, so the issue
    // you named outranks rows that merely mention it.
    const exact = scoreCommand('web-20', cmd({ id: 'x', label: 'Open it', tag: 'WEB-20' })) ?? 0;
    const longer = scoreCommand('web-20', cmd({ id: 'y', label: 'Open it', tag: 'WEB-207' })) ?? 0;
    expect(exact - longer).toBeGreaterThan(20);
  });
});

describe('rankCommands', () => {
  const commands: Command[] = [
    cmd({ id: 'action:new-task', label: 'New task', top: true }),
    cmd({ id: 'action:theme', label: 'Switch to light theme', top: true }),
    cmd({ id: 'action:refresh-models', label: 'Refresh models' }),
    cmd({ id: 'page:/logs', group: 'pages', label: 'Go to Logs', hint: 'g l' }),
    cmd({ id: 'page:/settings', group: 'pages', label: 'Go to Settings' }),
    cmd({ id: 'issue:WEB-207', group: 'issues', label: 'Open WEB-207 — Login', tag: 'WEB-207' }),
    cmd({ id: 'issue:WEB-20', group: 'issues', label: 'Open WEB-20 — Docs', tag: 'WEB-20' }),
    cmd({ id: 'profile:coder', group: 'profiles', label: 'Open coder in Agents', tag: '@coder' }),
    cmd({ id: 'logs:search', group: 'logs', label: 'Search logs for “web”' }),
    cmd({ id: 'help:>', group: 'help', label: 'Actions', prefill: '>' }),
  ];
  const ids = (q: string, recents: string[] = []) =>
    rankCommands(commands, q, recents).map((c) => c.id);

  it('empty query: recents first (as the Recent group), then top actions not already shown', () => {
    const rows = rankCommands(commands, '', ['page:/logs', 'action:theme', 'gone']);
    expect(rows.map((c) => c.id)).toEqual(['page:/logs', 'action:theme', 'action:new-task']);
    expect(rows.slice(0, 2).every((c) => c.group === 'recent')).toBe(true);
  });

  it('a prefix alone lists its scope in source order', () => {
    expect(ids('>')).toEqual(['action:new-task', 'action:theme', 'action:refresh-models']);
    expect(ids('/')).toEqual(['page:/logs', 'page:/settings']);
    expect(ids('@')).toEqual(['profile:coder']);
    expect(ids('?')).toEqual(['help:>']);
  });

  it('fuzzy: wb207 ranks WEB-207 first', () => {
    expect(ids('wb207')[0]).toBe('issue:WEB-207');
  });

  it('an exact id outranks a longer id with the same prefix', () => {
    expect(ids('web-20').slice(0, 2)).toEqual(['issue:WEB-20', 'issue:WEB-207']);
  });

  it('a prefix narrows the matches', () => {
    expect(ids('#web')).toEqual(['issue:WEB-207', 'issue:WEB-20', 'logs:search']);
    expect(ids('>web')).toEqual([]);
  });

  it('keeps rows of a group together, groups ordered by their best row, search-logs last', () => {
    const rows = rankCommands(commands, 'o', []);
    const groups = rows.map((c) => c.group);
    const seen: string[] = [];
    for (const g of groups) if (seen[seen.length - 1] !== g) seen.push(g);
    expect(new Set(seen).size).toBe(seen.length);
    expect(rows[rows.length - 1].id).toBe('logs:search');
  });

  it('a recent command ranks above an equal match', () => {
    expect(ids('go to')[0]).toBe('page:/logs');
    expect(ids('go to', ['page:/settings'])[0]).toBe('page:/settings');
  });

  it('no match: an empty list (search-logs aside)', () => {
    expect(ids('zzzz-nothing')).toEqual(['logs:search']);
  });
});
