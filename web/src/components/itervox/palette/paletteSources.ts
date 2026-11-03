// Command palette sources: pure builders that turn data the dashboard
// already has (the snapshot, TanStack Query caches) into commands. Each
// takes a `go` callback for navigation so it stays testable without a
// router. Availability lives here too: a builder returns nothing for what the
// caller cannot use.

import type { RulesLayerTab } from '../../../store/uiStore';
import type {
  AutomationDef,
  MemoryEntry,
  ProfileDef,
  RunningRow,
  Rule,
  SessionRow,
  TrackerIssue,
} from '../../../types/schemas';
import type { SettingsFieldDef } from '../../../lib/settingsSchema';
import { settingsSectionTitle } from '../../../pages/Settings/schema/settingsSections';
import { PAGES } from '../commandPalettePages';
import { SCOPE_PREFIXES, type Command } from './paletteModel';

type Go = (to: string) => void;
const enc = encodeURIComponent;

export function pageCommands(go: Go): Command[] {
  return PAGES.map((p) => ({
    id: `page:${p.path}`,
    group: 'pages',
    label: `Go to ${p.label}`,
    keywords: p.path,
    hint: p.keys,
    run: () => {
      go(p.path);
    },
  }));
}

export function helpCommands(): Command[] {
  return SCOPE_PREFIXES.map((p) => ({
    id: `help:${p.prefix}`,
    group: 'help',
    tag: p.prefix,
    label: p.label,
    prefill: p.scope === 'help' ? '?' : p.prefix,
    run: () => undefined,
  }));
}

export function issueCommands(
  issues: readonly Pick<TrackerIssue, 'identifier' | 'title' | 'state'>[],
  open: (identifier: string) => void,
): Command[] {
  return issues.map((issue) => ({
    id: `issue:${issue.identifier}`,
    group: 'issues',
    tag: issue.identifier,
    label: `Open ${issue.identifier} — ${issue.title}`,
    keywords: issue.state,
    run: () => {
      open(issue.identifier);
    },
  }));
}

export function runCommands(
  running: readonly Pick<RunningRow, 'identifier'>[],
  pause: (identifier: string) => void,
  discard: (identifier: string) => void,
): Command[] {
  return running.flatMap((row): Command[] => [
    {
      id: `pause:${row.identifier}`,
      group: 'runs',
      label: `Pause ${row.identifier}`,
      run: () => {
        pause(row.identifier);
      },
      confirm: {
        prompt: `Pause ${row.identifier}? The running agent is stopped.`,
        confirmLabel: `Yes, pause ${row.identifier}`,
      },
    },
    {
      id: `discard:${row.identifier}`,
      group: 'runs',
      danger: true,
      label: `Discard ${row.identifier}`,
      run: () => {
        discard(row.identifier);
      },
      confirm: {
        prompt: `Discard ${row.identifier}? The run stops and the issue moves back to the backlog.`,
        confirmLabel: `Yes, discard ${row.identifier}`,
      },
    },
  ]);
}

/**
 * "Search logs for <text>": opens Logs on the first issue the text matches,
 * else on the issue in view. Its sidebar search keeps the text when the
 * Logs page's own filter would match that issue with it, else the id.
 */
export function logsCommand(
  text: string,
  target: Pick<TrackerIssue, 'identifier' | 'title'> | null,
  go: Go,
  setLogsSearch: (search: string) => void,
): Command[] {
  if (text === '' || !target) return [];
  const haystack = `${target.identifier} ${target.title}`.toLowerCase();
  return [
    {
      id: 'logs:search',
      group: 'logs',
      label: `Search logs for “${text}”`,
      detail: target.identifier,
      run: () => {
        setLogsSearch(haystack.includes(text.toLowerCase()) ? text : target.identifier);
        go(`/logs/${enc(target.identifier)}`);
      },
    },
  ];
}

export function profileCommands(
  profileDefs: Readonly<Record<string, ProfileDef>>,
  canEdit: boolean,
  go: Go,
): Command[] {
  return Object.keys(profileDefs)
    .sort((a, b) => a.localeCompare(b))
    .flatMap((name): Command[] => {
      const def = profileDefs[name];
      const detail = [def.backend, def.enabled === false ? 'inactive' : '']
        .filter(Boolean)
        .join(' · ');
      const open: Command = {
        id: `profile:${name}`,
        group: 'profiles',
        tag: `@${name}`,
        label: `Open ${name} in Agents`,
        detail: detail || undefined,
        run: () => {
          go(`/agents#profile-${enc(name)}`);
        },
      };
      if (!canEdit) return [open];
      return [
        open,
        {
          id: `profile-edit:${name}`,
          group: 'profiles',
          tag: `@${name}`,
          label: `Edit profile ${name}`,
          run: () => {
            go(`/agents?editProfile=${enc(name)}`);
          },
        },
      ];
    });
}

export function automationCommands(automations: readonly AutomationDef[], go: Go): Command[] {
  return automations.map((a) => ({
    id: `automation:${a.id}`,
    group: 'automations',
    label: `Open automation ${a.id}`,
    keywords: `${a.trigger.type} ${a.profile}`,
    detail: `${a.trigger.type} · ${a.profile}${a.enabled ? '' : ' · off'}`,
    run: () => {
      go(`/automations?openAutomation=${enc(a.id)}`);
    },
  }));
}

const RULE_LAYERS: readonly RulesLayerTab[] = ['user', 'project', 'local'];

export function ruleCommands(
  layers: readonly { name: string; rules: readonly Rule[] }[],
  open: (layer: RulesLayerTab) => void,
): Command[] {
  return layers.flatMap((layer) => {
    const name = RULE_LAYERS.find((l) => l === layer.name);
    if (!name) return [];
    return layer.rules.map(
      (rule): Command => ({
        id: `rule:${name}:${rule.id}`,
        group: 'rules',
        tag: rule.id,
        label: rule.text,
        keywords: rule.description,
        detail: `${rule.severity} · ${name}`,
        run: () => {
          open(name);
        },
      }),
    );
  });
}

const MEMORY_LABEL_MAX = 90;

export function memoryCommands(
  entries: readonly MemoryEntry[],
  open: (id: string) => void,
): Command[] {
  return entries.map((e) => ({
    id: `memory:${e.id}`,
    group: 'memory',
    label: e.text.length > MEMORY_LABEL_MAX ? `${e.text.slice(0, MEMORY_LABEL_MAX)}…` : e.text,
    keywords: [...e.tags, ...e.profiles].join(' ') || undefined,
    detail: e.type,
    run: () => {
      open(e.id);
    },
  }));
}

export function settingsCommands(fields: readonly SettingsFieldDef[], go: Go): Command[] {
  return fields.map((f) => ({
    id: `setting:${f.key}`,
    group: 'settings',
    label: f.label,
    keywords: `${f.key} ${settingsSectionTitle(f.section)} ${f.group}`,
    detail: f.key,
    run: () => {
      go(`/settings?setting=${enc(f.key)}`);
    },
  }));
}

export function sessionCommands(
  sessions: readonly SessionRow[],
  keyOf: (s: SessionRow) => string,
  labelOf: (s: SessionRow) => string,
  go: Go,
): Command[] {
  return sessions.flatMap((s): Command[] => {
    const key = keyOf(s);
    if (!key) return [];
    const label = labelOf(s);
    const status = s.status.replace(/_/g, ' ');
    return [
      {
        id: `session:${key}`,
        group: 'sessions',
        label: s.title && s.title !== label ? `${label} — ${s.title}` : label,
        keywords: [status, s.profile, s.backend, s.workerHost].filter(Boolean).join(' '),
        detail: status,
        run: () => {
          go(`/sessions?id=${enc(key)}`);
        },
      },
    ];
  });
}
