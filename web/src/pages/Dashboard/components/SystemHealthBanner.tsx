import { Banner } from '../../../components/ui/Banner';
import { useItervoxStore } from '../../../store/itervoxStore';
import { fmtBytes } from '../../../utils/format';

/**
 * System visibility — the dashboard's machine warnings: free space on the
 * workspace volume below workspace.disk_warn_free_gb (a warning) or below
 * workspace.disk_pause_free_gb (dispatch pauses until it recovers), and
 * high memory pressure. Renders nothing while the machine is fine or the
 * daemon has no run monitor.
 */
export function SystemHealthBanner() {
  const machine = useItervoxStore((s) => s.snapshot?.system?.machine);
  if (!machine) return null;
  const disk = machine.disk;
  const notices: { key: string; tone: 'warning' | 'danger'; text: string }[] = [];
  if (disk && machine.diskLow) {
    const where = `${fmtBytes(disk.freeBytes)} free of ${fmtBytes(disk.totalBytes)} on the workspace volume (${disk.path})`;
    notices.push(
      machine.diskCritical
        ? {
            key: 'disk',
            tone: 'danger',
            text: `Disk almost full: ${where}. New runs are paused until free space is back above ${fmtBytes(machine.diskPauseBytes)}.`,
          }
        : {
            key: 'disk',
            tone: 'warning',
            text: `Low disk space: ${where}, below the ${fmtBytes(machine.diskWarnBytes)} warning threshold.`,
          },
    );
  }
  if (machine.memPressure === 'high') {
    notices.push({
      key: 'memory',
      tone: 'warning',
      text: `Memory pressure is high: ${fmtBytes(machine.memAvailableBytes)} available of ${fmtBytes(machine.memTotalBytes)}.`,
    });
  }
  if (notices.length === 0) return null;
  return (
    <div className="space-y-2" data-testid="system-health">
      {notices.map((n) => (
        <Banner key={n.key} tone={n.tone} data-testid={`system-health-${n.key}`}>
          {n.text}
        </Banner>
      ))}
    </div>
  );
}
