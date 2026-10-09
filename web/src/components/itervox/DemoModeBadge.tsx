// Shown in the header while `itervox demo` serves fake issues and a scripted
// agent (#76), so a screenshot or screen share is never mistaken for a real
// project.
export function DemoModeBadge() {
  return (
    <span
      data-testid="header-demo-mode"
      role="status"
      title="itervox demo: fake issues and a scripted agent. Nothing is sent to a tracker."
      className="bg-theme-warning-soft text-theme-warning-text shrink-0 rounded px-2 py-0.5 font-mono text-xs font-semibold"
    >
      Demo mode
    </span>
  );
}
