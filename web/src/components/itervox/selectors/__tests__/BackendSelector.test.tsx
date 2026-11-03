import { describe, it, expect, vi } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/react';
import { BackendSelector } from '../BackendSelector';

describe('BackendSelector', () => {
  it('renders editable dropdown by default', () => {
    render(<BackendSelector value="claude" onChange={vi.fn()} />);
    expect(screen.getByRole('combobox')).toBeInTheDocument();
  });

  it('renders read-only badge when readOnly is true', () => {
    render(<BackendSelector value="claude" onChange={vi.fn()} readOnly />);
    expect(screen.queryByRole('combobox')).toBeNull();
    expect(screen.getByText('claude')).toBeInTheDocument();
  });

  it('shows Backend label by default', () => {
    render(<BackendSelector value="claude" onChange={vi.fn()} />);
    expect(screen.getByText('Backend:')).toBeInTheDocument();
  });

  it('hides label when showLabel is false', () => {
    render(<BackendSelector value="claude" onChange={vi.fn()} showLabel={false} />);
    expect(screen.queryByText('Backend:')).toBeNull();
    expect(screen.getByRole('combobox')).toBeInTheDocument();
  });

  it('offers every backend the daemon accepts', () => {
    render(<BackendSelector value="claude" onChange={vi.fn()} />);
    const options = screen
      .getAllByRole('option')
      .map((o) => [o.getAttribute('value'), o.textContent]);
    expect(options).toEqual([
      ['claude', 'Claude'],
      ['codex', 'Codex'],
      ['opencode', 'OpenCode'],
      ['cursor', 'Cursor'],
      ['custom', 'Custom'],
    ]);
  });

  it('selects a new backend', () => {
    const onChange = vi.fn();
    render(<BackendSelector value="claude" onChange={onChange} defaultOptionLabel="Default" />);
    fireEvent.change(screen.getByRole('combobox'), { target: { value: 'opencode' } });
    expect(onChange).toHaveBeenCalledWith('opencode');
    fireEvent.change(screen.getByRole('combobox'), { target: { value: 'custom' } });
    expect(onChange).toHaveBeenCalledWith('custom');
  });

  it('fires onChange when selection changes', () => {
    const onChange = vi.fn();
    render(<BackendSelector value="claude" onChange={onChange} />);
    fireEvent.change(screen.getByRole('combobox'), { target: { value: 'codex' } });
    expect(onChange).toHaveBeenCalledWith('codex');
  });

  it('reflects the selected value', () => {
    render(<BackendSelector value="codex" onChange={vi.fn()} />);
    expect(screen.getByRole('combobox')).toHaveValue('codex');
  });

  it('renders with sm size variant', () => {
    render(<BackendSelector value="claude" onChange={vi.fn()} size="sm" />);
    const select = screen.getByRole('combobox');
    expect(select.className).toContain('text-[10px]');
  });

  it('renders with md size variant', () => {
    render(<BackendSelector value="claude" onChange={vi.fn()} size="md" />);
    const select = screen.getByRole('combobox');
    expect(select.className).toContain('text-xs');
  });

  it('read-only badge uses sm size classes', () => {
    render(<BackendSelector value="claude" onChange={vi.fn()} readOnly size="sm" />);
    const badge = screen.getByText('claude');
    expect(badge.className).toContain('text-[10px]');
  });
});
