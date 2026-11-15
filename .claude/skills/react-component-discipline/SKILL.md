---
name: react-component-discipline
description: Use when creating or editing any `.tsx` file under `web/src/components/` or `web/src/pages/`, when adding a new React hook under `web/src/hooks/`, or when the user asks for a new UI component. Enforces component size, state-layer boundaries (Zustand vs TanStack Query vs SSE snapshot), and reuse of existing itervox UI primitives.
---

# React Component Discipline (itervox web/)

Sharp rules for `.tsx` work in this repo. Read before writing.

## 1. Component size budget

- Target **<200 lines per `.tsx` file**.
- When a component crosses the budget, **extract sub-components into sibling files immediately** — do not defer.
- Pages live in `web/src/pages/<Name>/index.tsx`; reusable components in `web/src/components/itervox/` or `web/src/components/ui/`.

## 2. State layer routing (the rule that matters most)

Three non-overlapping layers. Pick exactly one per piece of state.

- **Server state** (`/api/v1/*` fetches): TanStack Query via `web/src/queries/*.ts`. Use `useQuery` / `useMutation` with the existing optimistic-update + rollback pattern from `queries/issues.ts`. **Never** put server state into Zustand.
- **Real-time SSE snapshot**: the orchestrator `StateSnapshot` arrives via `useItervoxSSE` and lives in `itervoxStore`. Read with a narrow selector per field (`useItervoxStore((s) => s.snapshot?.running ?? EMPTY_RUNNING)`) or `useShallow` over an object of the fields you use. **Never** subscribe to the whole snapshot (`(s) => s.snapshot`): `generatedAt` changes on every SSE frame, so the component re-renders on each one. `pnpm lint` runs `web/scripts/snapshotSelectorGuard.mjs`, which fails on it outside the store and SSE modules (#211). **Settings mutations must call `refreshSnapshot()`, never `patchSnapshot()`** — `patchSnapshot` will be overwritten by the next SSE tick.
- **Ephemeral UI state** (open/closed, filters, view mode, search): a Zustand store like `uiStore.ts`. **Do not** use `useState` in a page component for anything that should survive navigation.

## 3. Reuse existing primitives — grep first

Before creating any new component, grep `web/src/components/itervox/` and `web/src/components/ui/`. Common primitives already shipping:

`IssueCard`, `BoardColumn`, `RunningSessionsTable`, `SessionAccordion`, `IssueDetailSlide`, `AgentInfoModal`, `FilterPills`, `HostPool`, `NarrativeFeed`, `PausedIssuePanel`, `ProjectSelector`, `RateLimitBar`, `RetryQueueTable`, `ReviewQueueSection`, `TagInput`, plus `Modal`, `Card`, `LiveIndicator`, `Terminal`, `SlidePanel`, `Toast` in `ui/`.

Extending an existing primitive beats creating a new one.

## 4. Types from Zod, never inline

Canonical type source: `web/src/types/schemas.ts`. Use `z.infer<typeof SomeSchema>` or the exported aliases (`TrackerIssue`, `StateSnapshot`, etc.). Do **not** redefine an interface inline for a shape that already has a Zod schema.

## 5. Toast API — string first, never an object

```ts
// Correct
useToastStore.getState().addToast('Failed to save', 'error');

// WRONG — silently renders [object Object]
useToastStore.getState().addToast({ message: 'x', type: 'error' });
```

Inside effects/callbacks call `useToastStore.getState()` — do not invoke the hook there.

## 6. `EMPTY_*` module-level constants over `useMemo(() => [], [])`

For empty arrays/objects passed to children, declare a stable module-level reference:

```ts
const EMPTY_ISSUES: readonly TrackerIssue[] = [];
```

Grep `EMPTY_` for existing examples. Do not recreate empty literals on every render.

## 7. Network and SSE go through the auth module

Never call raw `fetch()` or `new EventSource()` in components or hooks. See `.claude/skills/authed-transport/SKILL.md` for the transport rules — this skill just reminds you at component-creation time.

## 8. List keys: composite semantic keys

Use a stable composite like `` `${issue.identifier}-${session.id}` ``. Never `key={index}`.

## Verification before claiming done

Run from `web/`:

```bash
pnpm lint
pnpm test            # runs the new component's tests
pnpm test:coverage   # confirms the 70% global gate still passes
```

New components **must** ship with tests under a sibling `__tests__/` directory — the coverage gate is 70% and silently dropping below it will fail CI.
