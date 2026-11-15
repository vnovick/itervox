import type { ActivityReport, RunUsage } from '../../../../types/schemas';

export const usage: RunUsage = {
  runId: 'run-1',
  identifier: 'ENG-1',
  live: true,
  cpuPercent: 82.5,
  cpuSeconds: 41.2,
  rssBytes: 734_003_200,
  peakRssBytes: 912_261_120,
  processes: 6,
  peakProcesses: 9,
  diskReadBytes: 1_048_576,
  diskWriteBytes: 3_145_728,
  diskIoKnown: true,
  netConnections: 2,
  topCommands: [
    { pid: 10, name: 'node', args: 'cli.js --print', cpuPercent: 61.2, rssBytes: 536_870_912 },
    { pid: 11, name: 'git', args: 'status', cpuPercent: 0.4, rssBytes: 8_388_608 },
  ],
  samples: 30,
};

export const report: ActivityReport = {
  identifier: 'ENG-1',
  runId: 'run-1',
  live: true,
  mode: 'sampled',
  platform: 'linux',
  supports: { commands: true, files: true, connections: true },
  notes: ['Sampled every 2s from /proc: short-lived processes may be missed.'],
  workspace: '/ws/ENG-1',
  commands: [
    { name: 'git', args: 'status --short', pid: 4300, count: 3, firstAt: 'a', lastAt: 'b' },
    { name: 'go', args: 'test ./...', pid: 4301, count: 1, firstAt: 'a', lastAt: 'b' },
  ],
  files: [
    { path: '/ws/ENG-1/main.go', write: true, count: 2, firstAt: 'a', lastAt: 'b' },
    {
      path: '/home/me/.npmrc',
      write: true,
      outsideWorkspace: true,
      count: 1,
      firstAt: 'a',
      lastAt: 'b',
    },
  ],
  connections: [
    {
      protocol: 'tcp',
      remote: '140.82.112.3:443',
      state: 'ESTABLISHED',
      command: 'git',
      count: 1,
      firstAt: 'a',
      lastAt: 'b',
    },
  ],
  dropped: { commands: 0, files: 4, connections: 0 },
  runs: [
    { runId: 'run-1', startedAt: '2026-11-02T10:00:00Z', live: true },
    { runId: 'run-0', startedAt: '2026-11-01T10:00:00Z' },
  ],
};
