import { useItervoxStore } from '../store/itervoxStore';
import { useFocusIssue } from '../hooks/useFocusIssue';

/** Embedded top bar title: `project · ISSUE` (the issue on the issue view). */
export function EmbeddedTitle() {
  const projectName = useItervoxStore((s) => s.snapshot?.projectName ?? '');
  const focus = useFocusIssue();
  return (
    <span
      data-testid="embedded-title"
      className="text-theme-text max-w-[40%] min-w-0 shrink-0 truncate text-[15px] font-bold sm:text-base"
    >
      {projectName || 'Itervox'}
      {focus && (
        <span className="text-theme-muted font-mono text-sm font-medium">
          {' · '}
          {focus.identifier}
        </span>
      )}
    </span>
  );
}
