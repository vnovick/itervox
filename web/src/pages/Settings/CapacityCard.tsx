import { useState } from 'react';
import { useItervoxStore } from '../../store/itervoxStore';
import { useSettingsActions } from '../../hooks/useSettingsActions';
import { IconButton } from '../../components/ui/IconButton';

export function CapacityCard() {
  const maxConcurrentAgents = useItervoxStore((s) => s.snapshot?.maxConcurrentAgents ?? 0);
  const { bumpWorkers } = useSettingsActions();
  const [adjusting, setAdjusting] = useState(false);

  const handleBump = async (delta: number) => {
    if (adjusting) return;
    setAdjusting(true);
    await bumpWorkers(delta);
    setAdjusting(false);
  };

  return (
    <div>
      <p className="text-theme-text-secondary text-[13px]">
        <span className="text-theme-text block font-medium">Max concurrent agents</span>
        Maximum number of agents that can run at the same time across all hosts.
      </p>
      <div className="mt-3 flex items-center gap-3">
        <IconButton
          label="Decrease max concurrent agents"
          variant="secondary"
          className="h-11 text-lg"
          onClick={() => {
            void handleBump(-1);
          }}
          disabled={adjusting || maxConcurrentAgents <= 1}
        >
          −
        </IconButton>
        <span className="text-theme-text min-w-[30px] text-center text-[28px] font-bold tabular-nums">
          {maxConcurrentAgents}
        </span>
        <IconButton
          label="Increase max concurrent agents"
          variant="secondary"
          className="h-11 text-lg"
          onClick={() => {
            void handleBump(1);
          }}
          disabled={adjusting}
        >
          +
        </IconButton>
      </div>
    </div>
  );
}
