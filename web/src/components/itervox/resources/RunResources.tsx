import type { RunUsage } from '../../../types/schemas';
import { fmtBytes, fmtCpu } from '../../../utils/format';

/** How many child commands the block lists (the daemon sends at most 5). */
const COMMANDS_SHOWN = 5;

/**
 * System visibility — a run's resource use on the issue's Now panels: CPU
 * (percent of one core), memory (RSS summed over the run's processes, with
 * the peak), processes and, where known, disk I/O and connections, then the
 * busiest child commands. A finished run shows its last sample.
 */
export function RunResources({ usage }: { usage: RunUsage }) {
  const tiles: { label: string; value: string; sub?: string }[] = [
    { label: 'CPU', value: fmtCpu(usage.cpuPercent), sub: `${usage.cpuSeconds.toFixed(0)}s total` },
    {
      label: 'Memory',
      value: fmtBytes(usage.rssBytes),
      sub: `peak ${fmtBytes(usage.peakRssBytes)}`,
    },
    {
      label: 'Processes',
      value: String(usage.processes),
      sub: `peak ${String(usage.peakProcesses)}`,
    },
    usage.diskIoKnown
      ? {
          label: 'Disk written',
          value: fmtBytes(usage.diskWriteBytes),
          sub: `${fmtBytes(usage.diskReadBytes)} read`,
        }
      : { label: 'Connections', value: String(usage.netConnections) },
  ];
  const commands = usage.topCommands.slice(0, COMMANDS_SHOWN);
  return (
    <div className="space-y-2" data-testid="run-resources">
      <ul aria-label="Resource use" className="grid grid-cols-2 gap-2 sm:grid-cols-4">
        {tiles.map((t) => (
          <li key={t.label} className="stat-tile px-3 py-2">
            <span className="stat-tile-label">{t.label}</span>
            <span className="stat-tile-value text-lg">{t.value}</span>
            {t.sub && <span className="stat-tile-sub">{t.sub}</span>}
          </li>
        ))}
      </ul>
      {usage.diskIoKnown && usage.netConnections > 0 && (
        <p className="text-theme-text-secondary text-xs">
          {usage.netConnections} open network{' '}
          {usage.netConnections === 1 ? 'connection' : 'connections'}
        </p>
      )}
      {usage.remote && (
        <p className="text-theme-muted text-xs">
          Runs on an SSH worker host: only the local ssh client is measured here.
        </p>
      )}
      {commands.length > 0 && (
        <table className="data-table" aria-label="Top child commands">
          <thead>
            <tr>
              <th scope="col">Command</th>
              <th scope="col" className="text-right">
                CPU
              </th>
              <th scope="col" className="text-right">
                Memory
              </th>
            </tr>
          </thead>
          <tbody>
            {commands.map((c) => (
              <tr key={c.pid}>
                <td
                  className="max-w-0 truncate font-mono text-xs"
                  title={c.args ? `${c.name} ${c.args}` : c.name}
                >
                  <span className="font-semibold">{c.name}</span>
                  {c.args ? <span className="text-theme-text-secondary"> {c.args}</span> : null}
                </td>
                <td className="text-right text-xs tabular-nums">{fmtCpu(c.cpuPercent)}</td>
                <td className="text-right text-xs tabular-nums">{fmtBytes(c.rssBytes)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  );
}
