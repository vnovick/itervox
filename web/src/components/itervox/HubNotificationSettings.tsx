import { useState } from 'react';
import { Button } from '../ui/button';
import { Input } from '../ui/Input';
import { EmptyState } from '../ui/EmptyState';
import { HubNotificationMatrix } from './HubNotificationMatrix';
import {
  useHubNotifications,
  useSaveHubNotifications,
  useTestHubNotification,
} from '../../queries/hub';
import { useToastStore } from '../../store/toastStore';
import type { HubNotificationsResponse } from '../../types/schemas';
import {
  CHANNEL_LABELS,
  matrixProjects,
  setSnooze,
  toggleRoute,
} from '../../utils/hubNotifications';

// #229 — the hub's Notifications settings view (admin): a project × kind ×
// channel matrix, a snooze per project, the digest interval, and a test
// notification per channel. Saving PUTs the whole document; the hub routes.

export function HubNotificationSettings() {
  const { data, isLoading, error, dataUpdatedAt } = useHubNotifications();
  if (isLoading) return <p className="text-theme-text-secondary text-sm">Loading settings…</p>;
  if (error || !data) {
    return (
      <p role="alert" className="text-theme-danger-text text-sm">
        Could not load the notification settings: {error?.message ?? 'no data'} (an admin hub token
        is needed).
      </p>
    );
  }
  return <NotificationEditor key={dataUpdatedAt} initial={data} />;
}

function NotificationEditor({ initial }: { initial: HubNotificationsResponse }) {
  const [draft, setDraft] = useState(initial.settings);
  const [nowMs] = useState(() => Date.now());
  const save = useSaveHubNotifications();
  const test = useTestHubNotification();
  const dirty = draft !== initial.settings;
  const projects = matrixProjects(initial.projects, draft);

  const onSave = () => {
    save.mutate(draft, {
      onSuccess: () => {
        useToastStore.getState().addToast('Notification settings saved', 'success');
      },
      onError: (e) => {
        useToastStore.getState().addToast(e.message, 'error');
      },
    });
  };
  const onTest = (channel: string) => {
    test.mutate(channel, {
      onSuccess: () => {
        useToastStore
          .getState()
          .addToast(`Test notification sent via ${CHANNEL_LABELS[channel] ?? channel}`, 'success');
      },
      onError: (e) => {
        useToastStore.getState().addToast(e.message, 'error');
      },
    });
  };

  return (
    <section className="flex flex-col gap-4" aria-label="Notification settings">
      <div className="flex flex-wrap items-end gap-3">
        <div className="text-theme-text flex flex-col gap-1 text-xs">
          <label htmlFor="hub-digest-interval">Digest interval (minutes)</label>
          <Input
            id="hub-digest-interval"
            type="number"
            min={1}
            max={1440}
            className="w-28"
            value={draft.digest_interval_minutes}
            onChange={(e) => {
              setDraft({
                ...draft,
                digest_interval_minutes: Math.min(1440, Math.max(1, Number(e.target.value) || 1)),
              });
            }}
          />
        </div>
        <p className="text-theme-muted max-w-sm text-xs">
          Input required and guardrail approvals are sent at once; other events are batched into one
          digest per interval.
        </p>
        <Button
          className="ml-auto"
          variant="primary"
          disabled={!dirty || save.isPending}
          onClick={onSave}
        >
          {save.isPending ? 'Saving…' : 'Save'}
        </Button>
      </div>
      <div className="flex flex-wrap items-center gap-2" aria-label="Test notifications">
        {initial.channels.map((ch) => (
          <Button
            key={ch.name}
            size="sm"
            disabled={!ch.available || test.isPending}
            title={ch.available ? undefined : `${CHANNEL_LABELS[ch.name] ?? ch.name} is not set up`}
            onClick={() => {
              onTest(ch.name);
            }}
          >
            Send test {CHANNEL_LABELS[ch.name] ?? ch.name}
          </Button>
        ))}
      </div>
      {projects.length === 0 && (
        <EmptyState
          title="No projects yet"
          description="Projects appear here once they join the hub."
        />
      )}
      {projects.map((p) => (
        <HubNotificationMatrix
          key={p}
          project={p}
          settings={draft}
          kinds={initial.kinds}
          channels={initial.channels}
          nowMs={nowMs}
          onToggle={(kind, channel, on) => {
            setDraft(toggleRoute(draft, p, kind, channel, on));
          }}
          onSnooze={(until) => {
            setDraft(setSnooze(draft, p, until));
          }}
        />
      ))}
    </section>
  );
}
