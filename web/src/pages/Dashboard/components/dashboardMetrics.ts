function startOfTodayMs(now: number = Date.now()): number {
  const d = new Date(now);
  d.setHours(0, 0, 0, 0);
  return d.getTime();
}

export function automationsFiredToday(
  rows: Array<{ automationId?: string; finishedAt: string; startedAt?: string }>,
  now: number = Date.now(),
): number {
  const start = startOfTodayMs(now);
  let count = 0;
  for (const row of rows) {
    if (!row.automationId) continue;
    const t = Date.parse(row.finishedAt);
    if (!Number.isNaN(t) && t >= start) count++;
  }
  return count;
}
