import { describe, it, expect } from 'vitest';
import { __testing } from '../useSettingsActions';

const { extractServerMessage } = __testing;

describe('extractServerMessage (T-10)', () => {
  it('returns the message field from a structured JSON error body', async () => {
    const res = new Response(
      JSON.stringify({ code: 'invalid_cron', message: 'invalid cron expression: bad token' }),
      { status: 400, headers: { 'Content-Type': 'application/json' } },
    );
    expect(await extractServerMessage(res)).toBe('invalid cron expression: bad token');
  });

  it('returns undefined for a plain-text body', async () => {
    const res = new Response('boom', { status: 400 });
    expect(await extractServerMessage(res)).toBeUndefined();
  });

  it('returns undefined when JSON has no message field', async () => {
    const res = new Response(JSON.stringify({ code: 'oops' }), {
      status: 400,
      headers: { 'Content-Type': 'application/json' },
    });
    expect(await extractServerMessage(res)).toBeUndefined();
  });

  it('returns undefined when message is not a string', async () => {
    const res = new Response(JSON.stringify({ message: 42 }), {
      status: 400,
      headers: { 'Content-Type': 'application/json' },
    });
    expect(await extractServerMessage(res)).toBeUndefined();
  });

  it('does not consume the original response body (clone)', async () => {
    const res = new Response(JSON.stringify({ message: 'hello' }), {
      status: 400,
      headers: { 'Content-Type': 'application/json' },
    });
    await extractServerMessage(res);
    // The original res must still be readable.
    const fromOriginal = (await res.json()) as { message: string };
    expect(fromOriginal.message).toBe('hello');
  });
});
