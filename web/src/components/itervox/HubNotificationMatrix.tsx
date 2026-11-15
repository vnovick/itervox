import { Card } from '../ui/Card/Card';
import { Button } from '../ui/button';
import { Select } from '../ui/Select';
import Badge from '../ui/badge/Badge';
import type { HubNotificationSettings, HubNotificationsResponse } from '../../types/schemas';
import {
  CHANNEL_LABELS,
  KIND_LABELS,
  SNOOZE_CHOICES,
  effectiveRoute,
  snoozedUntil,
} from '../../utils/hubNotifications';

// #229 — one project's notification routes (kind × channel) and its snooze,
// on the hub's Notifications settings view.

interface Props {
  project: string;
  settings: HubNotificationSettings;
  kinds: HubNotificationsResponse['kinds'];
  channels: HubNotificationsResponse['channels'];
  nowMs: number;
  onToggle: (kind: string, channel: string, on: boolean) => void;
  onSnooze: (untilIso: string | null) => void;
}

export function HubNotificationMatrix({
  project,
  settings,
  kinds,
  channels,
  nowMs,
  onToggle,
  onSnooze,
}: Props) {
  const until = snoozedUntil(settings, project, nowMs);
  return (
    <Card padding="md" className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center gap-2">
        <h3 className="text-theme-text font-mono text-sm font-semibold">{project}</h3>
        {until ? (
          <>
            <Badge size="sm" color="warning">
              Snoozed until {until.toLocaleString()}
            </Badge>
            <Button
              size="sm"
              variant="ghost"
              onClick={() => {
                onSnooze(null);
              }}
            >
              Unsnooze
            </Button>
          </>
        ) : (
          <Select
            aria-label={`Snooze ${project}`}
            className="ml-auto w-auto"
            value=""
            onChange={(e) => {
              const minutes = Number(e.target.value);
              if (minutes > 0) onSnooze(new Date(nowMs + minutes * 60_000).toISOString());
            }}
          >
            <option value="">Snooze…</option>
            {SNOOZE_CHOICES.map((c) => (
              <option key={c.minutes} value={c.minutes}>
                {c.label}
              </option>
            ))}
          </Select>
        )}
      </div>
      <div className="overflow-x-auto">
        <table className="w-full text-xs" aria-label={`${project} notification routes`}>
          <thead>
            <tr className="text-theme-muted text-left">
              <th scope="col" className="py-1 pr-2 font-medium">
                Event
              </th>
              {channels.map((ch) => (
                <th key={ch.name} scope="col" className="px-2 py-1 text-center font-medium">
                  {CHANNEL_LABELS[ch.name] ?? ch.name}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {kinds.map((k) => {
              const route = effectiveRoute(settings, project, k.name);
              return (
                <tr key={k.name} className="border-theme-line border-t">
                  <th scope="row" className="text-theme-text py-1 pr-2 text-left font-normal">
                    {KIND_LABELS[k.name] ?? k.name}
                    {k.urgent && <span className="text-theme-muted ml-1">(urgent)</span>}
                  </th>
                  {channels.map((ch) => (
                    <td key={ch.name} className="px-2 py-1 text-center">
                      <input
                        type="checkbox"
                        aria-label={`${project} ${KIND_LABELS[k.name] ?? k.name} via ${CHANNEL_LABELS[ch.name] ?? ch.name}`}
                        checked={route.includes(ch.name)}
                        onChange={(e) => {
                          onToggle(k.name, ch.name, e.target.checked);
                        }}
                      />
                    </td>
                  ))}
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
    </Card>
  );
}
