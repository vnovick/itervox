import { useMemo } from 'react';
import { useShallow } from 'zustand/react/shallow';
import { useItervoxStore } from '../../../store/itervoxStore';
import { useUIStore } from '../../../store/uiStore';
import { useMemory } from '../../../queries/memory';
import { useRules } from '../../../queries/rules';
import { sessionKey, sessionLabel, useSessions } from '../../../queries/sessions';
import { useSettingsSchema } from '../../../queries/settingsLayers';
import { flattenSettingsSchema } from '../../../lib/settingsSchema';
import { useScopeAllows } from '../../../hooks/useCallerScope';
import { EMPTY_AUTOMATIONS, EMPTY_PROFILE_DEFS } from '../../../utils/constants';
import type { Command } from './paletteModel';
import {
  automationCommands,
  memoryCommands,
  profileCommands,
  ruleCommands,
  sessionCommands,
  settingsCommands,
} from './paletteSources';

/**
 * The palette's searchable places beyond pages and issues: agent profiles
 * and automations (from the snapshot), rules, memory entries, settings
 * fields and sessions (TanStack Query). The palette is mounted only while
 * open, so these queries run on open — never in the main entry — and reuse
 * any cache a page already filled. Settings fields are listed for admins
 * only, the scope that sees the All settings section.
 */
export function usePaletteSources(go: (to: string) => void): Command[] {
  const { profileDefs, automations } = useItervoxStore(
    useShallow((s) => ({
      profileDefs: s.snapshot?.profileDefs ?? EMPTY_PROFILE_DEFS,
      automations: s.snapshot?.automations ?? EMPTY_AUTOMATIONS,
    })),
  );
  const isAdmin = useScopeAllows('admin');
  const memory = useMemory();
  const rules = useRules();
  const schema = useSettingsSchema(isAdmin);
  const { sessions } = useSessions({ poll: false });

  const memoryData = memory.data;
  const rulesData = rules.data;
  const schemaData = schema.data;

  return useMemo(() => {
    const ui = useUIStore.getState();
    const fields = isAdmin && schemaData ? flattenSettingsSchema(schemaData) : [];
    return [
      ...profileCommands(profileDefs, isAdmin, go),
      ...automationCommands(automations, go),
      ...ruleCommands(rulesData?.layers ?? [], (layer) => {
        ui.setRulesLayer(layer);
        ui.setRulesView('form');
        go('/rules');
      }),
      ...memoryCommands(memoryData?.entries ?? [], (id) => {
        ui.setMemoryTab('entries');
        ui.setMemorySelectedId(id);
        go('/memory');
      }),
      ...settingsCommands(fields, go),
      ...sessionCommands(sessions, sessionKey, sessionLabel, go),
    ];
  }, [profileDefs, automations, isAdmin, rulesData, memoryData, schemaData, sessions, go]);
}
