import { useEffect } from 'react';
import { useUIStore } from '../../../store/uiStore';
import type { IssueDetailTab } from './IssueDetailTabs';

/**
 * guardrails.lanes — when an approval's "Why" link asks for this issue's
 * Lanes view (uiStore.issueLanesFor), switch the issue detail to its
 * Changes tab; the tab then opens on Lanes and clears the request.
 */
export function useLanesTabRequest(
  selectedIdentifier: string | null,
  setTab: (tab: IssueDetailTab) => void,
): void {
  const lanesFor = useUIStore((s) => s.issueLanesFor);
  useEffect(() => {
    if (lanesFor !== null && lanesFor === selectedIdentifier) setTab('changes');
  }, [lanesFor, selectedIdentifier, setTab]);
}
