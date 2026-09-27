import { create } from 'zustand';
import { replaceEqualDeep } from '@tanstack/react-query';
import type { StateSnapshot } from '../types/schemas';
import { StateSnapshotSchema } from '../types/schemas';
import { authedFetch } from '../auth/authedFetch';
import { UnauthorizedError } from '../auth/UnauthorizedError';
import { reportClientError } from '../lib/clientErrorReporter';

const MAX_LOG_LINES = 500;

import {
  SCHEMA_DRIFT_BANNER_THRESHOLD,
  describeSnapshotParseFailure,
  type SchemaDriftInfo,
  type SnapshotSource,
} from './schemaDrift';

export { SCHEMA_DRIFT_BANNER_THRESHOLD } from './schemaDrift';

interface ItervoxState {
  snapshot: StateSnapshot | null;
  logs: string[];
  /**
   * CORE-069 — client id of the newest line in `logs` (monotonic, never
   * reset, so ids stay unique across clearLogs). Line `logs[i]` has id
   * `logSeq - logs.length + 1 + i`; NarrativeFeed keys its rows on it.
   */
  logSeq: number;
  sseConnected: boolean;
  selectedIdentifier: string | null;
  /** Cross-page active issue — persists across Timeline/Logs/Dashboard navigation */
  activeIssueId: string | null;
  /**
   * ms epoch of the last SSE message received, including keepalives.
   * CORE-023 — this is the "is the channel alive" signal, distinct from
   * lastSnapshotAt (the age of the displayed data): a keepalive proves the
   * connection is live without refreshing what's on screen.
   */
  lastMessageAt: number | null;
  /**
   * ms epoch of the last successfully parsed StateSnapshot, from either SSE
   * or the polling fallback. Drives the "data as of Ns ago" stale banner.
   */
  lastSnapshotAt: number | null;
  /**
   * CORE-047 — consecutive snapshot parse failures across SSE, poll and
   * refreshSnapshot. Reset only by a successfully parsed snapshot (never by a
   * keepalive). The SchemaDriftBanner shows at SCHEMA_DRIFT_BANNER_THRESHOLD.
   */
  schemaDriftCount: number;
  lastSchemaDrift: SchemaDriftInfo | null;
}

interface ItervoxActions {
  setSnapshot: (s: StateSnapshot) => void;
  appendLog: (line: string) => void;
  /** Appends a batch of lines in order with one store notification (CORE-075). */
  appendLogs: (lines: readonly string[]) => void;
  clearLogs: () => void;
  setSseConnected: (connected: boolean) => void;
  setSelectedIdentifier: (id: string | null) => void;
  setActiveIssueId: (id: string | null) => void;
  patchSnapshot: (patch: Partial<StateSnapshot>) => void;
  refreshSnapshot: () => Promise<void>;
  setLastMessageAt: (ts: number) => void;
  setLastSnapshotAt: (ts: number) => void;
  /**
   * Records one failed snapshot parse. Returns false (and records nothing)
   * when err is not a JSON/schema failure.
   */
  recordSnapshotParseFailure: (err: unknown, source: SnapshotSource) => boolean;
  /** Clears the drift streak; call only after a snapshot parsed successfully. */
  noteSnapshotParsed: () => void;
}

export type ItervoxStore = ItervoxState & ItervoxActions;

export const useItervoxStore = create<ItervoxStore>((set, get) => ({
  snapshot: null,
  logs: [],
  logSeq: 0,
  sseConnected: false,
  selectedIdentifier: null,
  activeIssueId: null,
  lastMessageAt: null,
  lastSnapshotAt: null,
  schemaDriftCount: 0,
  lastSchemaDrift: null,

  setSnapshot: (snapshot) => {
    set((state) => {
      // CORE-074 — structural sharing: unchanged branches keep their
      // identity, so selectors over them do not re-render their consumers.
      // The daemon stamps generatedAt on every build, so in production the
      // root still changes on every push; only a fully deep-equal payload
      // returns the same root, and then `state` itself is returned because
      // a new partial object would still notify every subscriber.
      const next = replaceEqualDeep(state.snapshot, snapshot);
      if (next === state.snapshot) return state;
      return { snapshot: next };
    });
  },

  appendLog: (line) => {
    get().appendLogs([line]);
  },

  appendLogs: (lines) => {
    if (lines.length === 0) return;
    set((state) => {
      const merged = [...state.logs, ...lines];
      return {
        logs: merged.length > MAX_LOG_LINES ? merged.slice(merged.length - MAX_LOG_LINES) : merged,
        logSeq: state.logSeq + lines.length,
      };
    });
  },

  clearLogs: () => {
    set({ logs: [] });
  },
  setSseConnected: (sseConnected) => {
    set({ sseConnected });
  },
  setSelectedIdentifier: (selectedIdentifier) => {
    set({ selectedIdentifier });
  },
  setActiveIssueId: (activeIssueId) => {
    set({ activeIssueId });
  },

  patchSnapshot: (patch) => {
    set((state) => ({
      snapshot: { ...(state.snapshot ?? {}), ...patch } as StateSnapshot,
    }));
  },

  refreshSnapshot: async () => {
    try {
      const res = await authedFetch('/api/v1/state');
      if (!res.ok) return;
      const data: StateSnapshot = StateSnapshotSchema.parse(await res.json());
      set((state) => ({
        // CORE-074 — same structural sharing as setSnapshot.
        snapshot: replaceEqualDeep(state.snapshot, data),
        lastSnapshotAt: Date.now(),
        schemaDriftCount: 0,
      }));
    } catch (err) {
      if (err instanceof UnauthorizedError) return; // AuthGate will handle.
      // CORE-047: a parse failure is counted (and surfaced) in production too.
      get().recordSnapshotParseFailure(err, 'refresh');
      if (import.meta.env.DEV) {
        console.warn('[itervox] refreshSnapshot failed — state may be stale', err);
      }
    }
  },

  setLastMessageAt: (ts) => {
    set({ lastMessageAt: ts });
  },
  setLastSnapshotAt: (ts) => {
    set({ lastSnapshotAt: ts });
  },

  recordSnapshotParseFailure: (err, source) => {
    const failure = describeSnapshotParseFailure(err);
    if (!failure) return false;
    const count = get().schemaDriftCount + 1;
    set({
      schemaDriftCount: count,
      lastSchemaDrift: { ...failure, source, at: Date.now() },
    });
    // Report once per streak, when the banner appears (CORE-048); the
    // reporter's own dedupe/throttle bounds anything beyond that.
    if (count === SCHEMA_DRIFT_BANNER_THRESHOLD) {
      reportClientError({
        kind: 'schema',
        message: `snapshot ${failure.reason} failure via ${source}: ${failure.detail}`,
      });
    }
    return true;
  },

  noteSnapshotParsed: () => {
    if (get().schemaDriftCount !== 0) set({ schemaDriftCount: 0 });
  },
}));
