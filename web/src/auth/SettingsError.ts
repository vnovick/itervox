import { z } from 'zod';

/**
 * Zod schema matching the wire shape produced by the Go server's
 * writeError() / writeAutomationValidationError() helpers:
 *
 *   { error: { code: "duplicate_automation_id", message: "...", field?: "id" } }
 *
 * The optional `field` discriminates field-level validation errors that the
 * UI can pin to a specific input (e.g. AutomationFormModal) vs. global errors
 * that bubble up as a toast.
 */
export const ServerErrorSchema = z.object({
  error: z.object({
    code: z.string(),
    message: z.string(),
    field: z.string().optional(),
  }),
});

export type ServerErrorBody = z.infer<typeof ServerErrorSchema>;

/**
 * Typed error class for non-2xx responses on settings endpoints. Carries the
 * structured `{code, message, field?}` so callers can:
 *   - Render `message` as a toast (current default behavior).
 *   - Pin field-level errors (`field === 'cron'`) to a specific input via
 *     `<FieldError>` instead of a generic toast.
 *
 * Built only when the response body parses cleanly against ServerErrorSchema;
 * non-JSON or unrecognised shapes still fall through to the legacy
 * extractServerMessage path so this is a strict-add, not a behavior swap.
 */
export class SettingsError extends Error {
  readonly code: string;
  readonly field?: string;

  constructor(code: string, message: string, field?: string) {
    super(message);
    this.name = 'SettingsError';
    this.code = code;
    this.field = field;
  }

  /**
   * Try to parse a Response body into a SettingsError. Returns null when the
   * body is not JSON or doesn't match the expected shape — caller should fall
   * back to a generic error path.
   *
   * Clones the Response so the caller can still read the body downstream if
   * needed.
   */
  static async fromResponse(res: Response): Promise<SettingsError | null> {
    try {
      const data: unknown = await res.clone().json();
      const parsed = ServerErrorSchema.safeParse(data);
      if (!parsed.success) return null;
      const { code, message, field } = parsed.data.error;
      return new SettingsError(code, message, field);
    } catch {
      return null;
    }
  }
}
