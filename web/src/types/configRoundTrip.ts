/**
 * CORE-047 round 2 — round-tripping config values this bundle does not know.
 *
 * A stale dashboard bundle can talk to a newer daemon. For DISPLAY-ONLY
 * snapshot fields an unknown enum value may fall back (tolerantEnum). For
 * fields the dashboard writes back with PUT/POST, a fallback would be data
 * loss, so the parsed value keeps a safe typed stand-in for rendering AND the
 * raw original, and every write path restores the raw original:
 *
 * - automations[].trigger.type (+ the whole raw trigger and raw policy,
 *   whose fields are trigger-specific), filter.matchMode,
 *   policy.switchToBackend → `AutomationDef.unknownFields`, restored by
 *   `automationDefToWire` in useSettingsActions.setAutomations(Typed);
 * - profileDefs[].allowedActions → `ProfileDef.unknownAllowedActions`,
 *   re-appended by useSettingsActions.upsertProfile.
 *
 * The UI shows these values read-only ("Unknown trigger 'x' (from a newer
 * server)") and never lets a form overwrite them.
 */

/** A raw automation trigger as the daemon sent it. */
export interface RawAutomationTrigger {
  type: string;
  cron?: string;
  timezone?: string;
  state?: string;
}

/** A raw automation policy (JSON scalars only on the wire). */
export type RawAutomationPolicy = Record<string, string | number | boolean>;

/**
 * A JSON value as the daemon sent it. Deliberately not a recursive type:
 * react-hook-form's DeepPartial over a recursive JSON type is "excessively
 * deep" for tsc -b.
 */
export type JsonValue = string | number | boolean | null | object;

/** Keys this bundle does not know, per automation object, kept verbatim. */
export interface AutomationExtraKeys {
  top?: Record<string, JsonValue>;
  trigger?: Record<string, JsonValue>;
  filter?: Record<string, JsonValue>;
  policy?: Record<string, JsonValue>;
}

/** Raw values of automation fields this bundle does not know. */
export interface AutomationUnknownFields {
  /**
   * Keys this bundle does not know at all (a newer daemon's new filter or
   * policy knob). z.object strips them on parse, so they are captured here
   * and merged back on save; keys the bundle does know always win.
   */
  extra?: AutomationExtraKeys;
  /** The raw trigger, when trigger.type is unknown. */
  trigger?: RawAutomationTrigger;
  /** The raw policy, kept whenever the trigger is unknown. */
  policy?: RawAutomationPolicy;
  /** The raw filter.matchMode, when unknown. */
  matchMode?: string;
  /** The raw policy.switchToBackend, when unknown. */
  switchToBackend?: string;
}

interface WireableAutomation {
  trigger: { type: string; cron?: string; timezone?: string; state?: string };
  filter?: Record<string, unknown>;
  policy?: Record<string, unknown>;
  unknownFields?: AutomationUnknownFields;
}

/** Human text for an unknown value, e.g. "Unknown trigger 'x' (from a newer server)". */
export function unknownValueLabel(what: string, raw: string): string {
  return `Unknown ${what} '${raw}' (from a newer server)`;
}

/**
 * Returns the automation as it must be sent to the daemon: raw unknown values
 * restored, the client-only `unknownFields` removed.
 */
export function automationDefToWire<T extends WireableAutomation>(
  def: T,
): Omit<T, 'unknownFields'> {
  const { unknownFields: unknown, ...rest } = def;
  if (!unknown) return rest;
  const out: Record<string, unknown> & Omit<T, 'unknownFields'> = { ...rest };
  if (unknown.trigger) {
    out.trigger = { ...unknown.trigger };
    out.policy = unknown.policy ? { ...unknown.policy } : undefined;
  } else if (unknown.switchToBackend !== undefined && rest.trigger.type === 'rate_limited') {
    out.policy = { ...(rest.policy ?? {}), switchToBackend: unknown.switchToBackend };
  }
  if (unknown.matchMode !== undefined) {
    out.filter = { ...(rest.filter ?? {}), matchMode: unknown.matchMode };
  }
  const extra = unknown.extra;
  if (extra) {
    // Unknown keys fill in around the known ones, never over them.
    const merge = (known: unknown, add: Record<string, JsonValue> | undefined) =>
      add ? { ...add, ...(known as Record<string, unknown> | undefined) } : known;
    if (extra.top) Object.assign(out, { ...extra.top, ...out });
    out.trigger = merge(out.trigger, extra.trigger) as T['trigger'];
    if (extra.filter) out.filter = merge(out.filter, extra.filter) as T['filter'];
    if (extra.policy) out.policy = merge(out.policy, extra.policy) as T['policy'];
  }
  return out;
}

/** Splits the keys of obj that are not in known into a separate record. */
export function extraKeys(
  obj: Record<string, unknown>,
  known: readonly string[],
): Record<string, JsonValue> | undefined {
  const extra: Record<string, JsonValue> = {};
  for (const [k, v] of Object.entries(obj)) {
    if (!known.includes(k)) extra[k] = v as JsonValue;
  }
  return Object.keys(extra).length > 0 ? extra : undefined;
}

/** Merges the daemon's unknown allowed actions back into a profile save. */
export function withUnknownAllowedActions(
  actions: readonly string[] | undefined,
  unknownActions: readonly string[] | undefined,
): string[] {
  const out = [...(actions ?? [])];
  for (const a of unknownActions ?? []) {
    if (!out.includes(a)) out.push(a);
  }
  return out;
}

/** The unknown automation values as notice items (trigger, match mode, backend). */
export function unknownAutomationItems(
  unknown: AutomationUnknownFields | undefined,
): { what: string; raw: string }[] {
  if (!unknown) return [];
  const items: { what: string; raw: string }[] = [];
  if (unknown.trigger) items.push({ what: 'trigger', raw: unknown.trigger.type });
  if (unknown.matchMode !== undefined) items.push({ what: 'match mode', raw: unknown.matchMode });
  if (unknown.switchToBackend !== undefined) {
    items.push({ what: 'backend', raw: unknown.switchToBackend });
  }
  return items;
}
