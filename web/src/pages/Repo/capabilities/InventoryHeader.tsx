// Inventory header: scan time, current/stale badge and Re-scan (#200; moved
// from Settings → SkillsCard).

import type { SkillsInventory } from '../../../types/schemas';

interface InventoryHeaderProps {
  inventory: SkillsInventory;
  scanning: boolean;
  onRescan: () => void;
}

export function InventoryHeader({ inventory, scanning, onRescan }: InventoryHeaderProps) {
  const inventoryStale = inventory.Stale === true;
  return (
    <div className="border-theme-line bg-theme-panel rounded-lg border p-4">
      <div className="flex items-center justify-between gap-3">
        <div>
          <div className="flex flex-wrap items-center gap-2">
            <p className="text-theme-text text-sm font-medium">Inventory</p>
            <span
              role="status"
              className={`inline-flex items-center rounded-full border px-2 py-0.5 text-[10px] font-bold uppercase ${
                inventoryStale
                  ? 'border-theme-warning/40 bg-theme-warning-soft text-theme-warning-text'
                  : 'border-theme-success/40 bg-theme-success-soft text-theme-success-text'
              }`}
            >
              {inventoryStale ? 'Stale' : 'Tracked files current'}
            </span>
          </div>
          <p className="text-theme-muted mt-0.5 text-xs">
            Last scanned: {new Date(inventory.ScanTime).toLocaleString()}
          </p>
          <p className="text-theme-muted mt-1 max-w-xl text-[11px]">
            {inventoryStale
              ? 'Tracked config or inventory files changed since the last scan. Click Re-scan to refresh recommendations.'
              : 'Tracked config and inventory files are unchanged since the last scan. Click Re-scan after adding new roots or changing untracked paths.'}
          </p>
        </div>
        <button
          onClick={onRescan}
          disabled={scanning}
          className="border-theme-line text-theme-text rounded border px-3 py-1.5 text-xs font-semibold hover:bg-[var(--bg-soft)] disabled:opacity-50"
        >
          {scanning ? 'Scanning…' : 'Re-scan'}
        </button>
      </div>
    </div>
  );
}
