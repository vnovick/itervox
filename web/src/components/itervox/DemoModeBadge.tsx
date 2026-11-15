// Shown in the header while `itervox demo` serves fake issues and a scripted
// agent (#76), so a screenshot or screen share is never mistaken for a real
// project.
export function DemoModeBadge() {
  return (
    <span
      data-testid="header-demo-mode"
      role="status"
      title="itervox demo: fake issues and a scripted agent. Nothing is sent to a tracker."
      className="pill pill-warn shrink-0 font-semibold"
    >
      Demo mode
    </span>
  );
}
