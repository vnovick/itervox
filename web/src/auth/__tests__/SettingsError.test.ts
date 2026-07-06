import { describe, expect, it } from 'vitest';
import { SettingsError } from '../SettingsError';

describe('SettingsError.fromResponse', () => {
  it('parses a wire body with code+message', async () => {
    const res = new Response(
      JSON.stringify({ error: { code: 'invalid_cron', message: 'invalid cron expression' } }),
      { status: 400, headers: { 'Content-Type': 'application/json' } },
    );
    const err = await SettingsError.fromResponse(res);
    expect(err).toBeInstanceOf(SettingsError);
    expect(err?.code).toBe('invalid_cron');
    expect(err?.message).toBe('invalid cron expression');
    expect(err?.field).toBeUndefined();
  });

  it('parses field-level errors when present', async () => {
    const res = new Response(
      JSON.stringify({
        error: { code: 'duplicate_automation_id', message: 'duplicate id', field: 'id' },
      }),
      { status: 400 },
    );
    const err = await SettingsError.fromResponse(res);
    expect(err?.field).toBe('id');
  });

  it('returns null for a non-JSON body', async () => {
    const res = new Response('plain text error', { status: 500 });
    expect(await SettingsError.fromResponse(res)).toBeNull();
  });

  it('returns null for JSON that does not match the schema', async () => {
    const res = new Response(JSON.stringify({ msg: 'wrong shape' }), { status: 400 });
    expect(await SettingsError.fromResponse(res)).toBeNull();
  });

  it('does not consume the response body (clones first)', async () => {
    const res = new Response(JSON.stringify({ error: { code: 'x', message: 'y' } }), {
      status: 400,
    });
    await SettingsError.fromResponse(res);
    // Original response body must still be readable.
    const body = await res.text();
    expect(body).toContain('x');
  });
});
