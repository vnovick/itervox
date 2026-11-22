export type IssueDetailTab = 'details' | 'changes' | 'evidence';

const TABS: readonly { id: IssueDetailTab; label: string }[] = [
  { id: 'details', label: 'Details' },
  { id: 'changes', label: 'Changes' },
  { id: 'evidence', label: 'Evidence' },
];

const DEFAULT_TABS: readonly IssueDetailTab[] = ['details', 'changes'];

/**
 * #215 — the issue detail's tab bar (Details | Changes | Evidence). `tabs`
 * names the ones the daemon serves (Details is always first).
 */
export function IssueDetailTabs({
  tab,
  onChange,
  tabs = DEFAULT_TABS,
}: {
  tab: IssueDetailTab;
  onChange: (tab: IssueDetailTab) => void;
  tabs?: readonly IssueDetailTab[];
}) {
  return (
    <div role="tablist" aria-label="Issue detail" className="segmented mx-6 mb-1 self-start">
      {TABS.filter((t) => t.id === 'details' || tabs.includes(t.id)).map((t) => (
        <button
          key={t.id}
          type="button"
          role="tab"
          id={`issue-tab-${t.id}`}
          aria-selected={tab === t.id}
          onClick={() => {
            onChange(t.id);
          }}
          className="min-h-8 px-4 py-1 text-sm"
        >
          {t.label}
        </button>
      ))}
    </div>
  );
}
