import { useQueryClient } from '@tanstack/react-query';
import { useLocation } from 'react-router';
import { useShallow } from 'zustand/react/shallow';
import { getApiContext } from '../../../auth/apiBase';
import { useOptionalTheme } from '../../../context/ThemeContext';
import { useScopeAllows } from '../../../hooks/useCallerScope';
import { useFocusIssue } from '../../../hooks/useFocusIssue';
import { useAnalyzeDeps } from '../../../queries/deps';
import { useSetDispatchPause } from '../../../queries/dispatch';
import { hubProjectHref, useHubProjects } from '../../../queries/hub';
import { refreshModels } from '../../../queries/models';
import { useSendReviewToAgent } from '../../../queries/review';
import { useItervoxStore } from '../../../store/itervoxStore';
import { useReviewDraftStore } from '../../../store/reviewDraftStore';
import { useUIStore } from '../../../store/uiStore';
import type { DiffResponse } from '../../../types/schemas';
import { DISPATCH_PAUSE_CAPABILITY } from '../../../pages/Dashboard/components/dispatchPauseModel';
import { actionCommands } from './paletteActions';
import type { Command } from './paletteModel';

/** The PR the issue's cached diff named (the Changes tab fills the cache). */
function cachedPrUrl(qc: ReturnType<typeof useQueryClient>, identifier: string): string | null {
  for (const [, data] of qc.getQueriesData<DiffResponse | null>({
    queryKey: ['issueDiff', identifier],
  })) {
    if (data && 'prUrl' in data && data.prUrl) return data.prUrl;
  }
  return null;
}

/** The palette's actions, each only when it can run (see paletteActions). */
export function usePaletteActions(go: (to: string) => void): Command[] {
  const { pathname } = useLocation();
  const qc = useQueryClient();
  const canOperate = useScopeAllows('operator');
  const isAdmin = useScopeAllows('admin');
  const theme = useOptionalTheme();
  const snap = useItervoxStore(
    useShallow((s) => ({
      pausable: s.snapshot?.capabilities?.includes(DISPATCH_PAUSE_CAPABILITY) ?? false,
      paused: (s.snapshot?.dispatchPaused ?? null) !== null,
      analyzer: s.snapshot?.depsAnalyzerProfile ?? '',
      analyzerDef: s.snapshot?.profileDefs?.[s.snapshot.depsAnalyzerProfile ?? ''],
      jobStatus: s.snapshot?.depsAnalyzeJob?.status,
      selected: s.selectedIdentifier,
    })),
  );
  const focus = useFocusIssue();
  const current = snap.selected ?? focus?.identifier ?? null;
  const draft = useReviewDraftStore(
    useShallow((s) => ({
      comments: current ? s.pending[current] : undefined,
      note: current ? (s.notes[current] ?? '') : '',
    })),
  );
  const dispatch = useSetDispatchPause();
  const analyze = useAnalyzeDeps();
  const send = useSendReviewToAgent(current ?? '');
  const api = getApiContext();
  const hubProject = api.mode === 'hubProject' ? api.project : null;
  const hub = useHubProjects(hubProject !== null);

  const projects = hub.data?.projects ?? [];
  const prUrl = current ? cachedPrUrl(qc, current) : null;
  const comments = draft.comments ?? [];
  const hasDraft = comments.length > 0 || draft.note.trim() !== '';
  const analyzerReady =
    snap.analyzer !== '' &&
    snap.analyzerDef !== undefined &&
    snap.analyzerDef.enabled !== false &&
    snap.jobStatus !== 'running' &&
    snap.jobStatus !== 'queued' &&
    !analyze.isPending;

  return actionCommands({
    newTask: canOperate
      ? () => {
          if (pathname === '/') useUIStore.getState().setNewTaskOpen(true);
          else go('/?newTask=1');
        }
      : null,
    dispatch:
      snap.pausable && canOperate
        ? {
            paused: snap.paused,
            set: (paused) => {
              dispatch.mutate({ paused });
            },
          }
        : null,
    theme: theme ? { current: theme.theme, toggle: theme.toggleTheme } : null,
    projects:
      hubProject !== null && projects.length > 1
        ? projects.filter((p) => p.proxied && p.name !== hubProject).map((p) => p.name)
        : [],
    switchProject: (name) => {
      window.location.assign(hubProjectHref(name));
    },
    analyzeDeps:
      canOperate && analyzerReady
        ? () => {
            analyze.mutate({});
          }
        : null,
    refreshModels: isAdmin
      ? () => {
          void refreshModels('all');
        }
      : null,
    pr:
      current && prUrl
        ? {
            identifier: current,
            open: () => {
              window.open(prUrl, '_blank', 'noopener,noreferrer');
            },
          }
        : null,
    review:
      current && canOperate && hasDraft
        ? {
            identifier: current,
            comments: comments.length,
            send: () => {
              // mutateAsync, not mutate's callbacks: the palette unmounts
              // as it closes, and the draft must still clear once sent.
              send
                .mutateAsync({
                  comments,
                  note: draft.note.trim(),
                  moveToActive: true,
                  alsoPostOnPR: prUrl !== null,
                })
                .then(() => {
                  useReviewDraftStore.getState().clear(current);
                })
                .catch(() => undefined);
            },
          }
        : null,
  });
}
