// CORE-094 — the general UI primitive layer.
import { createRef } from 'react';
import { describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { Button } from '../button';
import { IconButton } from '../IconButton';
import { Input } from '../Input';
import { Select } from '../Select';
import { Textarea } from '../Textarea';
import { ToggleChip } from '../ToggleChip';
import { Banner } from '../Banner';
import { EmptyState } from '../EmptyState';
import { cn } from '../cn';

describe('cn', () => {
  it('merges conflicting Tailwind classes so a caller override wins', () => {
    const hidden = Number('0') > 1;
    expect(cn('px-2 py-1', hidden && 'hidden', 'px-4')).toBe('py-1 px-4');
  });
});

describe('Button', () => {
  it('forwards ref, disabled and type', async () => {
    const ref = createRef<HTMLButtonElement>();
    const onClick = vi.fn();
    const { rerender } = render(
      <Button ref={ref} onClick={onClick}>
        Save
      </Button>,
    );
    const btn = screen.getByRole('button', { name: 'Save' });
    expect(ref.current).toBe(btn);
    // Defaults to type="button" so it never submits a surrounding form by accident.
    expect(btn).toHaveAttribute('type', 'button');
    rerender(
      <Button ref={ref} type="submit" disabled onClick={onClick}>
        Save
      </Button>,
    );
    expect(btn).toHaveAttribute('type', 'submit');
    expect(btn).toBeDisabled();
    await userEvent.click(btn);
    expect(onClick).not.toHaveBeenCalled();
  });

  it('merges a caller className over the variant classes', () => {
    render(
      <Button variant="ghost" size="sm" className="px-6">
        x
      </Button>,
    );
    const cls = screen.getByRole('button').className;
    expect(cls).toContain('px-6');
    expect(cls).not.toMatch(/\bpx-2\b/);
  });
});

describe('IconButton', () => {
  it('requires and exposes an accessible name', () => {
    render(<IconButton label="Close panel">×</IconButton>);
    expect(screen.getByRole('button', { name: 'Close panel' })).toHaveAttribute('type', 'button');
  });
});

describe('Input, Select and Textarea', () => {
  it('forward refs and mark invalid fields', () => {
    const inputRef = createRef<HTMLInputElement>();
    const selectRef = createRef<HTMLSelectElement>();
    const areaRef = createRef<HTMLTextAreaElement>();
    render(
      <>
        <Input ref={inputRef} aria-label="Name" invalid />
        <Select ref={selectRef} aria-label="Mode">
          <option value="a">A</option>
        </Select>
        <Textarea ref={areaRef} aria-label="Reply" />
      </>,
    );
    expect(inputRef.current).toBe(screen.getByRole('textbox', { name: 'Name' }));
    expect(screen.getByRole('textbox', { name: 'Name' })).toHaveAttribute('aria-invalid', 'true');
    expect(selectRef.current).toBe(screen.getByRole('combobox', { name: 'Mode' }));
    expect(areaRef.current).toBe(screen.getByRole('textbox', { name: 'Reply' }));
  });
});

describe('ToggleChip', () => {
  it('exposes aria-pressed', async () => {
    const onPressedChange = vi.fn();
    const { rerender } = render(
      <ToggleChip pressed={false} onPressedChange={onPressedChange}>
        warn
      </ToggleChip>,
    );
    const chip = screen.getByRole('button', { name: 'warn' });
    expect(chip).toHaveAttribute('aria-pressed', 'false');
    await userEvent.click(chip);
    expect(onPressedChange).toHaveBeenCalledWith(true);
    rerender(
      <ToggleChip pressed onPressedChange={onPressedChange}>
        warn
      </ToggleChip>,
    );
    expect(chip).toHaveAttribute('aria-pressed', 'true');
  });
});

describe('Banner', () => {
  it('uses role=alert for danger and role=status otherwise', () => {
    const { rerender } = render(<Banner tone="danger">Stream lost</Banner>);
    expect(screen.getByRole('alert')).toHaveTextContent('Stream lost');
    rerender(<Banner tone="info">Heads up</Banner>);
    expect(screen.getByRole('status')).toHaveTextContent('Heads up');
  });
});

describe('EmptyState', () => {
  it('renders title, description and action', () => {
    render(
      <EmptyState title="No logs" description="Waiting for output" action={<button>Go</button>} />,
    );
    expect(screen.getByText('No logs')).toBeInTheDocument();
    expect(screen.getByText('Waiting for output')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Go' })).toBeInTheDocument();
  });
});
