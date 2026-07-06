import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

// Module under test is imported dynamically in each test so we can reset its
// module-level `cached` variable by calling `vi.resetModules()` between
// cases. Without a reset, the first test's memoized list would bleed into
// subsequent tests and hide bugs in the fallback branch.

describe('getIANATimezones', () => {
  const originalSupportedValuesOf = (
    Intl as unknown as { supportedValuesOf?: (key: string) => string[] }
  ).supportedValuesOf;

  beforeEach(() => {
    vi.resetModules();
  });

  afterEach(() => {
    // Restore whatever the environment provides.
    (Intl as unknown as { supportedValuesOf?: (key: string) => string[] }).supportedValuesOf =
      originalSupportedValuesOf;
  });

  it('returns a non-empty list containing common IANA zones', async () => {
    const { getIANATimezones } = await import('../timezones');
    const zones = getIANATimezones();
    expect(zones.length).toBeGreaterThan(0);
    // Use widely-deployed zone names as the assertion anchors. Different
    // ICU versions disagree on "UTC" vs "Etc/UTC"; Europe/London and
    // America/New_York are present in every modern tz database.
    expect(zones).toContain('Europe/London');
    expect(zones).toContain('America/New_York');
  });

  it('memoizes the result (same reference on second call)', async () => {
    const { getIANATimezones } = await import('../timezones');
    const first = getIANATimezones();
    const second = getIANATimezones();
    expect(second).toBe(first);
  });

  it('returns a frozen array so callers cannot mutate the cache', async () => {
    const { getIANATimezones } = await import('../timezones');
    const zones = getIANATimezones();
    expect(Object.isFrozen(zones)).toBe(true);
  });

  it('returns zones in locale-ascending order', async () => {
    const { getIANATimezones } = await import('../timezones');
    const zones = getIANATimezones();
    for (let i = 1; i < zones.length; i++) {
      expect(zones[i - 1].localeCompare(zones[i])).toBeLessThanOrEqual(0);
    }
  });

  it('falls back to the curated list when Intl.supportedValuesOf is unavailable', async () => {
    // Simulate an older browser (pre-Chrome 99 / Firefox 93 / Safari 15.4)
    // by deleting the runtime method before importing the module.
    delete (Intl as unknown as { supportedValuesOf?: unknown }).supportedValuesOf;

    const { getIANATimezones } = await import('../timezones');
    const zones = getIANATimezones();

    // The fallback list is curated to ~18 common zones; verify it includes
    // the anchors that must always be present.
    expect(zones.length).toBeGreaterThanOrEqual(10);
    expect(zones).toContain('UTC');
    expect(zones).toContain('America/New_York');
    expect(zones).toContain('Europe/London');
  });
});
