import { useState } from 'react';
import { Button } from '../../components/ui/button';
import { useAttentionNotifications } from '../../hooks/useAttentionNotifications';

/**
 * CORE-096 — opt in to browser notifications for "needs your input" and
 * final failures. Permission is requested only from this button's click.
 */
export function BrowserNotificationsCard() {
  const { supported, enabled, enable, disable } = useAttentionNotifications({ watch: false });
  const [refused, setRefused] = useState(false);
  return (
    <div className="border-theme-line bg-theme-panel flex flex-wrap items-center justify-between gap-4 rounded-lg border p-4">
      <div className="min-w-0">
        <p className="text-theme-text text-sm font-medium">Notify me in this browser</p>
        <p className="text-theme-muted mt-0.5 text-xs">
          {supported
            ? 'A desktop notification when an issue needs your input or a run fails for good, only while this tab is in the background. Stored in this browser only.'
            : 'Not available here: notifications need HTTPS (or localhost) and permission that has not been blocked. The page title and the Dashboard badge still count what needs you.'}
        </p>
        {refused && (
          <p role="status" className="text-theme-warning-text mt-1 text-xs">
            The browser did not grant permission.
          </p>
        )}
      </div>
      {supported &&
        (enabled ? (
          <Button size="sm" onClick={disable}>
            Turn off
          </Button>
        ) : (
          <Button
            size="sm"
            variant="primary"
            onClick={() => {
              void enable().then((ok) => {
                setRefused(!ok);
              });
            }}
          >
            Enable notifications
          </Button>
        ))}
    </div>
  );
}
