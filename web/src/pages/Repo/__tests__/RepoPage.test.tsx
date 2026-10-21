// #200 — the Repo Analysis page: four sections, sub-tabs selected by the
// location hash (/repo#capabilities, #usage, #prompts, #readiness).
import { describe, expect, it, vi } from 'vitest';
import { fireEvent, screen } from '@testing-library/react';
import { render } from '../../../test/render';
import RepoAnalysis from '../index';
import { sectionFromHash } from '../repoSections';

vi.mock('../../../components/common/PageMeta', () => ({ default: () => null }));
vi.mock('../capabilities/SkillsCard', () => ({
  SkillsCard: () => <div data-testid="capabilities-body" />,
}));
vi.mock('../usage/UsageSection', () => ({
  UsageSection: () => <div data-testid="usage-body" />,
}));
vi.mock('../prompts/PromptVersionsSection', () => ({
  PromptVersionsSection: () => <div data-testid="prompts-body" />,
}));
vi.mock('../readiness/ReadinessSection', () => ({
  ReadinessSection: () => <div data-testid="readiness-body" />,
}));

const SECTIONS = [
  ['capabilities', 'Capabilities'],
  ['usage', 'Runtime usage and cost'],
  ['prompts', 'Prompt versions'],
  ['readiness', 'Agent readiness'],
] as const;

function activeSection(): string | undefined {
  return document.querySelector('section[data-active="true"]')?.id;
}

describe('Repo Analysis page (#200)', () => {
  it('renders the four sections, each with its heading and body', () => {
    render(<RepoAnalysis />, { route: '/repo' });
    expect(screen.getByRole('heading', { level: 1, name: 'Repo Analysis' })).toBeInTheDocument();
    for (const [key, label] of SECTIONS) {
      const region = screen.getByRole('region', { name: label });
      expect(region).toHaveAttribute('id', key);
      expect(screen.getByTestId(`${key}-body`)).toBeInTheDocument();
      expect(screen.getByRole('tab', { name: label })).toHaveAttribute('aria-controls', key);
    }
  });

  it('opens on Capabilities by default', () => {
    render(<RepoAnalysis />, { route: '/repo' });
    expect(screen.getByRole('tab', { name: 'Capabilities' })).toHaveAttribute(
      'aria-selected',
      'true',
    );
    expect(activeSection()).toBe('capabilities');
  });

  it.each(SECTIONS)('deep link /repo#%s selects that tab', (key, label) => {
    render(<RepoAnalysis />, { route: `/repo#${key}` });
    expect(screen.getByRole('tab', { name: label })).toHaveAttribute('aria-selected', 'true');
    expect(activeSection()).toBe(key);
    // Inactive sections are hidden on wide screens only (stacked on mobile).
    for (const [other] of SECTIONS) {
      if (other === key) continue;
      expect(document.getElementById(other)).toHaveClass('md:hidden');
    }
  });

  it('clicking a tab switches the section; arrow keys move between tabs', () => {
    render(<RepoAnalysis />, { route: '/repo' });
    fireEvent.click(screen.getByRole('tab', { name: 'Agent readiness' }));
    expect(activeSection()).toBe('readiness');
    fireEvent.keyDown(screen.getByRole('tab', { name: 'Agent readiness' }), { key: 'ArrowRight' });
    expect(activeSection()).toBe('capabilities');
    fireEvent.keyDown(screen.getByRole('tab', { name: 'Capabilities' }), { key: 'ArrowLeft' });
    expect(activeSection()).toBe('readiness');
  });

  it('maps unknown hashes to Capabilities', () => {
    expect(sectionFromHash('')).toBe('capabilities');
    expect(sectionFromHash('#nope')).toBe('capabilities');
    expect(sectionFromHash('#usage')).toBe('usage');
  });
});
