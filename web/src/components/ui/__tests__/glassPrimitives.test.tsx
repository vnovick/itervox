// Visual refresh — the shared surface primitives render the glass recipes
// from index.css (.glass-panel / .glass-card / .glass-sheet / .glass-scrim /
// .glass-chip / .pill-*), so pages inherit one look.
import { describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { Button } from '../button';
import { ConfirmButton } from '../button/ConfirmButton';
import { IconButton } from '../IconButton';
import { Card } from '../Card/Card';
import Badge from '../badge/Badge';
import { Modal, ConfirmModal } from '../modal';
import { SlidePanel } from '../SlidePanel/SlidePanel';
import { CopyButton } from '../CopyButton';
import { ToggleChip } from '../ToggleChip';
import { Input } from '../Input';
import { Banner } from '../Banner';

describe('Button variants', () => {
  it('secondary and glass render the glass chip; pill rounds fully', () => {
    render(
      <>
        <Button>Sec</Button>
        <Button variant="glass" pill>
          Glass
        </Button>
      </>,
    );
    const sec = screen.getByRole('button', { name: 'Sec' });
    expect(sec).toHaveClass('glass-chip');
    expect(sec.className).toContain('rounded-[var(--radius-sm)]');
    const glass = screen.getByRole('button', { name: 'Glass' });
    expect(glass).toHaveClass('glass-chip', 'rounded-full');
    expect(glass.className).not.toContain('rounded-[var(--radius-sm)]');
  });

  it('primary is the accent fill and destructive the solid danger fill, both white text', () => {
    render(
      <>
        <Button variant="primary">Go</Button>
        <Button variant="destructive">Stop</Button>
        <Button variant="danger">Soft</Button>
      </>,
    );
    expect(screen.getByRole('button', { name: 'Go' })).toHaveClass('bg-theme-accent', 'text-white');
    expect(screen.getByRole('button', { name: 'Stop' })).toHaveClass(
      'bg-theme-danger',
      'text-white',
    );
    expect(screen.getByRole('button', { name: 'Soft' })).toHaveClass(
      'bg-theme-danger-soft',
      'text-theme-danger-text',
    );
  });

  it('lg size is 40px tall', () => {
    render(<Button size="lg">Big</Button>);
    expect(screen.getByRole('button', { name: 'Big' })).toHaveClass('h-10');
  });

  it('IconButton is round by default and can opt out', () => {
    const { rerender } = render(<IconButton label="Close">×</IconButton>);
    expect(screen.getByRole('button', { name: 'Close' })).toHaveClass('rounded-full');
    rerender(
      <IconButton label="Close" pill={false}>
        ×
      </IconButton>,
    );
    expect(screen.getByRole('button', { name: 'Close' })).not.toHaveClass('rounded-full');
  });
});

describe('Card surfaces', () => {
  it.each([
    ['panel', 'glass-panel'],
    ['elevated', 'glass-panel'],
    ['card', 'glass-card'],
    ['default', 'glass-card'],
    ['subtle', 'subtle-fill'],
  ] as const)('%s renders %s', (variant, cls) => {
    const { container } = render(<Card variant={variant}>x</Card>);
    expect(container.firstChild).toHaveClass(cls);
    expect(container.firstChild).toHaveAttribute('data-variant', variant);
  });
});

describe('Badge', () => {
  it('renders a toned pill', () => {
    render(
      <>
        <Badge color="success">ok</Badge>
        <Badge color="warning">warn</Badge>
        <Badge variant="glass">glass</Badge>
      </>,
    );
    expect(screen.getByText('ok')).toHaveClass('pill', 'pill-ok');
    expect(screen.getByText('warn')).toHaveClass('pill', 'pill-warn');
    expect(screen.getByText('glass')).toHaveClass('pill', 'glass-chip');
  });
});

describe('Overlays', () => {
  it('Modal is a glass sheet over a scrim with a round glass close button', () => {
    render(
      <Modal isOpen onClose={vi.fn()} ariaLabel="Sheet">
        body
      </Modal>,
    );
    const dialog = screen.getByRole('dialog', { name: 'Sheet' });
    expect(dialog).toHaveClass('glass-sheet');
    expect(document.querySelector('.glass-scrim')).not.toBeNull();
    expect(screen.getByRole('button', { name: 'Close' })).toHaveClass('glass-chip', 'h-9', 'w-9');
  });

  it('ConfirmModal: glass Cancel and solid confirm, both 40px', () => {
    render(
      <ConfirmModal
        isOpen
        onClose={vi.fn()}
        onConfirm={vi.fn()}
        title="Pause all dispatch?"
        description="Nothing new starts."
        confirmLabel="Pause dispatch"
      />,
    );
    expect(screen.getByRole('dialog', { name: 'Pause all dispatch?' })).toHaveClass('glass-sheet');
    const cancel = screen.getByRole('button', { name: 'Cancel' });
    expect(cancel).toHaveClass('glass-chip', 'h-10');
    expect(screen.getByRole('button', { name: 'Pause dispatch' })).toHaveClass(
      'bg-theme-danger',
      'text-white',
      'h-10',
    );
    // Centred confirm dialogs carry no close button.
    expect(screen.queryByRole('button', { name: 'Close' })).toBeNull();
  });

  it('SlidePanel is an inset glass sheet with a glass close button', () => {
    render(
      <SlidePanel isOpen onClose={vi.fn()} title="Automation qa">
        body
      </SlidePanel>,
    );
    const dialog = screen.getByRole('dialog', { name: 'Automation qa' });
    expect(dialog).toHaveClass('glass-sheet', 'inset-y-3', 'right-3');
    expect(screen.getByTestId('slide-panel-overlay')).toHaveClass('glass-scrim');
    expect(screen.getByRole('button', { name: 'Close panel' })).toHaveClass('glass-chip');
  });
});

describe('ConfirmButton', () => {
  it('soft trigger, then a card row with a solid confirm and a Cancel', async () => {
    const onConfirm = vi.fn();
    render(
      <ConfirmButton
        label="Discard"
        confirmLabel="Yes, discard"
        pendingLabel="Discarding…"
        isPending={false}
        onConfirm={onConfirm}
      />,
    );
    const trigger = screen.getByRole('button', { name: 'Discard' });
    expect(trigger).toHaveClass('bg-theme-danger-soft', 'rounded-full');
    await userEvent.click(trigger);
    const yes = screen.getByRole('button', { name: 'Yes, discard' });
    expect(yes).toHaveClass('bg-theme-danger', 'text-white');
    expect(yes).toHaveFocus();
    expect(yes.closest('.glass-card')).not.toBeNull();
    await userEvent.click(yes);
    expect(onConfirm).toHaveBeenCalledTimes(1);
  });
});

describe('Small controls', () => {
  it('CopyButton is a glass pill', () => {
    render(<CopyButton text="x" label="Copy command" />);
    expect(screen.getByRole('button', { name: 'Copy command' })).toHaveClass('glass-chip');
  });

  it('ToggleChip is a pill, accent when pressed and subtle when not', () => {
    const { rerender } = render(
      <ToggleChip pressed onPressedChange={vi.fn()}>
        warn
      </ToggleChip>,
    );
    const chip = screen.getByRole('button', { name: 'warn' });
    expect(chip).toHaveClass('rounded-full', 'bg-theme-accent-soft');
    rerender(
      <ToggleChip pressed={false} onPressedChange={vi.fn()}>
        warn
      </ToggleChip>,
    );
    expect(chip).toHaveClass('subtle-fill', 'line-through');
  });

  it('Input is a card-white field with 12px corners and a hairline rim', () => {
    render(<Input aria-label="Host" />);
    expect(screen.getByRole('textbox', { name: 'Host' })).toHaveClass(
      'bg-theme-panel',
      'border-theme-line-strong',
      'rounded-[var(--radius-sm)]',
    );
  });

  it('Banner is a rounded soft tint', () => {
    render(<Banner tone="warning">Heads up</Banner>);
    expect(screen.getByRole('status')).toHaveClass(
      'rounded-[var(--radius-md)]',
      'bg-theme-warning-soft',
    );
  });
});
