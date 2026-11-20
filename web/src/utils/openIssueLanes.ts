import { useItervoxStore } from '../store/itervoxStore';
import { useUIStore } from '../store/uiStore';

/**
 * guardrails.lanes — open an issue's detail on Changes → Lanes: select the
 * issue (the URL follows through useIssueUrlSync) and ask its Changes tab
 * for the Lanes view.
 */
export function openIssueLanes(identifier: string): void {
  useUIStore.getState().setIssueLanesFor(identifier);
  useItervoxStore.getState().setSelectedIdentifier(identifier);
}
