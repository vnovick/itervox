// #211 — the light cron input that replaced antd + react-js-cron.
import { describe, expect, it, vi } from 'vitest';
import { useState } from 'react';
import { render, screen, fireEvent } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { CronPicker } from '../CronPicker';
import { CRON_PRESETS, parseCron } from '../../../../lib/cron';
import parityRaw from '../../../../../../internal/schedule/testdata/cron_parity.json?raw';

const fixture = JSON.parse(parityRaw) as { cases: { expr: string; valid: boolean }[] };

function Harness({ initial, onChange }: { initial: string; onChange?: (v: string) => void }) {
  const [value, setValue] = useState(initial);
  return (
    <>
      <CronPicker
        value={value}
        onChange={(next) => {
          setValue(next);
          onChange?.(next);
        }}
      />
      <output data-testid="value">{value}</output>
    </>
  );
}

const value = (): string => screen.getByTestId('value').textContent;

describe('CronPicker presets', () => {
  it('selects the preset matching the current value and shows its preview', () => {
    render(<Harness initial="0 9 * * 1-5" />);
    expect(screen.getByLabelText('Preset')).toHaveValue('weekdays');
    expect(screen.getByTestId('cron-preview')).toHaveTextContent(
      'At 09:00 on Monday through Friday',
    );
  });

  it.each(CRON_PRESETS.map((p) => [p.id, p.expr] as const))(
    'choosing preset %s writes %s, fills the fields and validates',
    async (id, expr) => {
      const onChange = vi.fn();
      render(<Harness initial="1 2 3 4 5" onChange={onChange} />);
      await userEvent.selectOptions(screen.getByLabelText('Preset'), id);
      expect(onChange).toHaveBeenLastCalledWith(expr);
      expect(value()).toBe(expr);
      expect(screen.getByTestId('cron-raw-input')).toHaveValue(expr);
      expect(screen.getByLabelText('Minute')).toHaveValue(expr.split(' ')[0]);
      expect(screen.getByLabelText('Weekday')).toHaveValue(expr.split(' ')[4]);
      expect(screen.queryByTestId('cron-error')).toBeNull();
    },
  );
});

describe('CronPicker custom expressions', () => {
  it('a raw expression updates the fields, the preview and drops to Custom', async () => {
    render(<Harness initial="0 9 * * 1-5" />);
    const raw = screen.getByTestId('cron-raw-input');
    await userEvent.clear(raw);
    await userEvent.type(raw, '30 18 1,15 * 0');
    expect(value()).toBe('30 18 1,15 * 0');
    expect(screen.getByLabelText('Preset')).toHaveValue('custom');
    expect(screen.getByLabelText('Day')).toHaveValue('1,15');
    expect(screen.getByTestId('cron-preview')).toHaveTextContent(
      'At 18:30 on day 1, 15 of the month or on Sunday',
    );
  });

  it('editing one field rewrites only that field of the expression', async () => {
    render(<Harness initial="0 9 * * 1-5" />);
    const hour = screen.getByLabelText('Hour');
    await userEvent.clear(hour);
    // While the field is empty the draft is kept and flagged, not replaced.
    expect(hour).toHaveValue('');
    expect(screen.getByTestId('cron-error')).toHaveTextContent('Hour is empty.');
    expect(hour).toHaveAttribute('aria-invalid', 'true');
    await userEvent.type(hour, '*/2');
    expect(value()).toBe('0 */2 * * 1-5');
    expect(screen.getByTestId('cron-preview')).toHaveTextContent(
      'At minute 0 past hour every 2 hours',
    );
  });

  it('shows a Quartz expression as its five Unix fields', () => {
    render(<Harness initial="0 0 9 ? * 1-5 *" />);
    expect(screen.getByLabelText('Minute')).toHaveValue('0');
    expect(screen.getByLabelText('Hour')).toHaveValue('9');
    expect(screen.getByLabelText('Day')).toHaveValue('*');
    expect(screen.getByTestId('cron-preview')).toHaveTextContent(
      'At 09:00 on Monday through Friday',
    );
  });
});

describe('CronPicker invalid input', () => {
  it.each([
    ['* 24 * * *', 'Hour', 'hour: value 24 out of range [0,23]'],
    ['* * * * MON', 'Weekday', 'day-of-week: invalid integer "MON"'],
    ['* * * JAN *', 'Month', 'month: invalid integer "JAN"'],
    ['*/0 * * * *', 'Minute', 'minute: invalid step value "0"'],
  ])('%s flags the %s field with the server message', (expr, label, message) => {
    render(<Harness initial={expr} />);
    expect(screen.getByTestId('cron-error')).toHaveTextContent(`Invalid cron: ${message}`);
    expect(screen.getByLabelText(label)).toHaveAttribute('aria-invalid', 'true');
    expect(screen.getByTestId('cron-raw-input')).toHaveAttribute('aria-invalid', 'true');
    expect(screen.queryByTestId('cron-preview')).toBeNull();
  });

  it.each(['@daily', '* * * *', '* * * * * *'])('rejects %s as a whole', (expr) => {
    render(<Harness initial={expr} />);
    expect(screen.getByTestId('cron-error')).toBeInTheDocument();
    for (const label of ['Minute', 'Hour', 'Day', 'Month', 'Weekday']) {
      expect(screen.getByLabelText(label)).toHaveAttribute('aria-invalid', 'false');
    }
  });
});

describe('CronPicker round-trip with the server parser', () => {
  it.each(fixture.cases.map((c) => [JSON.stringify(c.expr), c] as const))(
    'typed %s: the editor verdict matches internal/schedule',
    (_label, c) => {
      render(<Harness initial="* * * * *" />);
      fireEvent.change(screen.getByTestId('cron-raw-input'), { target: { value: c.expr } });
      expect(value()).toBe(c.expr);
      expect(screen.queryByTestId('cron-error') === null).toBe(c.valid);
    },
  );

  it('fields rebuilt from any valid fixture expression re-join to an expression that still parses', () => {
    for (const c of fixture.cases.filter((x) => x.valid)) {
      const { unmount } = render(<Harness initial={c.expr} />);
      // Re-emit the minute field unchanged: the editor rejoins all five fields.
      const minute = screen.getByLabelText('Minute');
      const current = (minute as HTMLInputElement).value;
      fireEvent.change(minute, { target: { value: `${current} ` } });
      expect(parseCron(value()).ok).toBe(true);
      unmount();
    }
  });
});
