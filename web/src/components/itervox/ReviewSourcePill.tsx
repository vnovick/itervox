// Shared pill rendered next to a review-queue identifier indicating whether
// the issue was just completed by a worker in this daemon session ("session"
// — green ✓) or was already in the review state when the daemon started
// ("tracker" — muted). Used both inside NotificationsView (review group)
// and inline in ReviewQueueSection.awaitingReview rows so the visual stays
// consistent across surfaces.

interface Props {
  source: 'session' | 'tracker';
}

export function ReviewSourcePill({ source }: Props) {
  if (source === 'session') {
    return (
      <span className="rounded bg-emerald-500/15 px-1.5 py-0.5 text-[10px] font-medium text-emerald-300">
        ✓ completed this session
      </span>
    );
  }
  return (
    <span className="text-theme-text-secondary border-theme-line rounded border px-1.5 py-0.5 text-[10px]">
      in review (tracker)
    </span>
  );
}
