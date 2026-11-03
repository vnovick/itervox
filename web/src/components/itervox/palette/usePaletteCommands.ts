import { useCallback, useMemo } from 'react';
import { useNavigate } from 'react-router';
import { useItervoxStore } from '../../../store/itervoxStore';
import { useUIStore } from '../../../store/uiStore';
import { useCancelIssue, useIssues, useTerminateIssue } from '../../../queries/issues';
import { useFocusIssue } from '../../../hooks/useFocusIssue';
import { EMPTY_RUNNING } from '../../../utils/constants';
import { parseQuery, scoreCommand, type Command } from './paletteModel';
import {
  helpCommands,
  issueCommands,
  logsCommand,
  pageCommands,
  runCommands,
} from './paletteSources';
import { usePaletteActions } from './usePaletteActions';
import { usePaletteSources } from './usePaletteSources';

/** Every command the palette offers for `query` (unranked, source order). */
export function usePaletteCommands(query: string): Command[] {
  const navigate = useNavigate();
  const go = useCallback(
    (to: string) => {
      void navigate(to);
    },
    [navigate],
  );
  const { data: issues } = useIssues();
  const running = useItervoxStore((s) => s.snapshot?.running ?? EMPTY_RUNNING);
  const selected = useItervoxStore((s) => s.selectedIdentifier);
  const setSelected = useItervoxStore((s) => s.setSelectedIdentifier);
  const focus = useFocusIssue();
  const pause = useCancelIssue();
  const discard = useTerminateIssue();
  const sources = usePaletteSources(go);
  const actions = usePaletteActions(go);

  const core = useMemo(
    () => ({
      pages: pageCommands(go),
      help: helpCommands(),
      issues: issueCommands(issues ?? [], setSelected),
      runs: runCommands(
        running,
        (id) => {
          pause.mutate(id);
        },
        (id) => {
          discard.mutate(id);
        },
      ),
    }),
    [go, issues, setSelected, running, pause, discard],
  );

  const { text } = parseQuery(query);
  const logs = useMemo(() => {
    if (text === '') return [];
    let target: { identifier: string; title: string } | null = null;
    let best = 0;
    for (const issue of issues ?? []) {
      const s = scoreCommand(text, {
        id: issue.identifier,
        group: 'issues',
        label: issue.title,
        tag: issue.identifier,
        run: () => undefined,
      });
      if (s !== null && s > best) {
        best = s;
        target = issue;
      }
    }
    const current = selected ?? focus?.identifier ?? null;
    if (!target && current) {
      target = issues?.find((i) => i.identifier === current) ?? { identifier: current, title: '' };
    }
    return logsCommand(text, target, go, useUIStore.getState().setLogsIssueSearch);
  }, [text, issues, selected, focus?.identifier, go]);

  return useMemo(
    () => [
      ...actions,
      ...core.pages,
      ...core.issues,
      ...core.runs,
      ...sources,
      ...logs,
      ...core.help,
    ],
    [actions, core, sources, logs],
  );
}
