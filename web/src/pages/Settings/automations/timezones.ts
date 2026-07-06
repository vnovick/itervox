// IANA timezone list, sourced from the browser's ICU data via
// Intl.supportedValuesOf('timeZone'). This is the modern standard and is
// supported by Chrome 99+, Firefox 93+, Safari 15.4+ (all widely-deployed
// targets at the time of writing).
//
// The daemon accepts any string that time.LoadLocation can parse, so even
// though this list is the typical dropdown source, free-form entries remain
// valid via the paired <input list="...">. Empty string means "use the daemon
// timezone" — the Go side falls back to time.Local.

const FALLBACK_TIMEZONES = [
  'UTC',
  'America/Los_Angeles',
  'America/Denver',
  'America/Chicago',
  'America/New_York',
  'America/Sao_Paulo',
  'Europe/London',
  'Europe/Paris',
  'Europe/Berlin',
  'Europe/Moscow',
  'Asia/Jerusalem',
  'Asia/Dubai',
  'Asia/Kolkata',
  'Asia/Singapore',
  'Asia/Tokyo',
  'Asia/Shanghai',
  'Australia/Sydney',
  'Pacific/Auckland',
];

let cached: readonly string[] | null = null;

export function getIANATimezones(): readonly string[] {
  if (cached !== null) return cached;

  const supportedValuesOf = (Intl as unknown as { supportedValuesOf?: (key: string) => string[] })
    .supportedValuesOf;
  const zones =
    typeof supportedValuesOf === 'function'
      ? supportedValuesOf('timeZone')
      : [...FALLBACK_TIMEZONES];

  // Sort so the dropdown is deterministic across browsers.
  zones.sort((a, b) => a.localeCompare(b));
  cached = Object.freeze(zones);
  return cached;
}
