import type { RefObject } from 'react';
import { Search } from 'lucide-react';
import { Button } from '../../ui/button';
import type { Command, CommandConfirm } from './paletteModel';

/**
 * The palette's confirm step. It keeps the palette's chrome: the header row
 * names the chosen command where the search field was, then the prompt.
 */
export function PaletteConfirm({
  command,
  confirm,
  confirmRef,
  onCancel,
  onConfirm,
}: {
  command: Command;
  confirm: CommandConfirm;
  confirmRef: RefObject<HTMLButtonElement | null>;
  onCancel: () => void;
  onConfirm: () => void;
}) {
  return (
    <div>
      <div className="border-theme-line flex items-center gap-3 border-b px-5 py-3.5">
        <Search aria-hidden="true" className="text-theme-text-secondary h-4 w-4 flex-shrink-0" />
        <span className="text-theme-text min-w-0 flex-1 truncate text-base">{command.label}</span>
        <kbd aria-hidden="true" className="tag font-mono">
          esc
        </kbd>
      </div>
      <div className="space-y-3 px-5 pt-4 pb-5">
        <p className="eyebrow text-theme-text-secondary">Confirm step</p>
        <p className="text-theme-text text-[15px] leading-normal">{confirm.prompt}</p>
        <div className="flex justify-end gap-2 pt-1">
          <Button variant="secondary" size="lg" pill onClick={onCancel}>
            Cancel
          </Button>
          <Button
            ref={confirmRef}
            variant={confirm.tone === 'primary' ? 'primary' : 'destructive'}
            size="lg"
            pill
            onClick={onConfirm}
          >
            {confirm.confirmLabel}
          </Button>
        </div>
      </div>
    </div>
  );
}
