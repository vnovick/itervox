import { useState } from 'react';
import { useItervoxStore } from '../../../store/itervoxStore';
import { ConfirmModal } from '../../../components/ui/modal/ConfirmModal';
import { useSetDispatchPause } from '../../../queries/dispatch';
import { OpsChip } from './OpsChip';
import { DISPATCH_PAUSE_CAPABILITY, dispatchPausedLabel } from './dispatchPauseModel';
import { useScopeAllows } from '../../../hooks/useCallerScope';

// #213 — the daemon-wide dispatch pause on the LiveOps strip: a "Pause
// dispatch" button while dispatch runs, a danger chip plus "Resume" while it
// is paused. Both ask for confirmation first. Hidden on daemons that do not
// advertise the dispatch_pause capability.

const BUTTON_CLASS =
  'border-theme-line text-theme-text-secondary hover:text-theme-text inline-flex min-h-9 flex-shrink-0 items-center rounded-[var(--radius-sm)] border px-2.5 py-1 text-[12px] font-medium whitespace-nowrap transition-colors disabled:opacity-50';

export function DispatchPauseToggle() {
  const supported = useItervoxStore(
    (s) => s.snapshot?.capabilities?.includes(DISPATCH_PAUSE_CAPABILITY) ?? false,
  );
  // #214 — pause/resume is operator scope; a viewer sees only the chip.
  const canOperate = useScopeAllows('operator');
  if (!supported) return null;
  return canOperate ? <DispatchPauseControl /> : <DispatchPausedChip />;
}

function DispatchPausedChip() {
  const paused = useItervoxStore((s) => s.snapshot?.dispatchPaused ?? null);
  return paused ? (
    <OpsChip
      danger
      testId="dispatch-paused-chip"
      label={dispatchPausedLabel(paused)}
      title="All dispatch is paused: running agents continue, nothing new starts."
    />
  ) : null;
}

function DispatchPauseControl() {
  const paused = useItervoxStore((s) => s.snapshot?.dispatchPaused ?? null);
  const [confirming, setConfirming] = useState(false);
  const mutation = useSetDispatchPause();

  const pausing = paused === null;
  const confirm = () => {
    mutation.mutate(
      { paused: pausing },
      {
        onSettled: () => {
          setConfirming(false);
        },
      },
    );
  };

  return (
    <>
      {paused && (
        <OpsChip
          danger
          testId="dispatch-paused-chip"
          label={dispatchPausedLabel(paused)}
          title="All dispatch is paused: running agents continue, nothing new starts."
        />
      )}
      <button
        type="button"
        className={BUTTON_CLASS}
        data-testid="dispatch-pause-toggle"
        aria-pressed={!pausing}
        disabled={mutation.isPending}
        onClick={() => {
          setConfirming(true);
        }}
      >
        {pausing ? 'Pause dispatch' : 'Resume dispatch'}
      </button>
      <ConfirmModal
        isOpen={confirming}
        onClose={() => {
          setConfirming(false);
        }}
        onConfirm={confirm}
        title={pausing ? 'Pause all dispatch?' : 'Resume dispatch?'}
        description={
          pausing
            ? 'Running agents continue. Nothing new starts — no dispatch, retries, reviewers, automations or input resumes — until you resume.'
            : 'Waiting issues, retries, reviews and automations start again as slots allow.'
        }
        confirmLabel={pausing ? 'Pause dispatch' : 'Resume dispatch'}
        variant={pausing ? 'danger' : 'primary'}
        isPending={mutation.isPending}
        pendingLabel={pausing ? 'Pausing…' : 'Resuming…'}
      />
    </>
  );
}
