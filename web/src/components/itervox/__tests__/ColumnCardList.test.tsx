// #211 — board columns over BOARD_VIRTUALIZE_THRESHOLD cards mount only the
// cards near the viewport.
import { describe, expect, it } from 'vitest';
import { fireEvent, render, screen } from '@testing-library/react';
import { BOARD_VIRTUALIZE_THRESHOLD, ColumnCardList } from '../BoardColumn/ColumnCardList';
import { cardWindow } from '../BoardColumn/cardWindow';
import { makeIssues } from '../../../test/fixtures/issues';
import type { TrackerIssue } from '../../../types/schemas';

const renderCard = (issue: TrackerIssue) => (
  <div key={issue.identifier} data-testid="card">
    {issue.identifier}
  </div>
);

describe('cardWindow', () => {
  it('starts at the top with overscan after the visible rows', () => {
    expect(
      cardWindow({ count: 500, scrollTop: 0, viewport: 800, rowHeight: 100, overscan: 5 }),
    ).toEqual({ start: 0, end: 14 });
  });

  it('follows the scroll offset with overscan on both sides', () => {
    expect(
      cardWindow({ count: 500, scrollTop: 10_000, viewport: 800, rowHeight: 100, overscan: 5 }),
    ).toEqual({ start: 95, end: 114 });
  });

  it('clamps at the end of the list', () => {
    expect(
      cardWindow({ count: 120, scrollTop: 50_000, viewport: 800, rowHeight: 100, overscan: 5 }),
    ).toEqual({ start: 119, end: 120 });
  });

  it('always includes keepIndex (the dragged card)', () => {
    expect(
      cardWindow({
        count: 500,
        scrollTop: 10_000,
        viewport: 800,
        rowHeight: 100,
        overscan: 5,
        keepIndex: 3,
      }),
    ).toEqual({ start: 3, end: 114 });
  });

  it('is empty for an empty list', () => {
    expect(
      cardWindow({ count: 0, scrollTop: 0, viewport: 800, rowHeight: 100, overscan: 5 }),
    ).toEqual({ start: 0, end: 0 });
  });
});

describe('ColumnCardList', () => {
  it(`renders every card up to the threshold (${String(BOARD_VIRTUALIZE_THRESHOLD)})`, () => {
    render(
      <ColumnCardList issues={makeIssues(BOARD_VIRTUALIZE_THRESHOLD)} renderCard={renderCard} />,
    );
    expect(screen.getAllByTestId('card')).toHaveLength(BOARD_VIRTUALIZE_THRESHOLD);
  });

  it('mounts only a window of cards above the threshold', () => {
    const { container } = render(
      <ColumnCardList issues={makeIssues(400)} renderCard={renderCard} />,
    );
    const cards = screen.getAllByTestId('card');
    expect(cards.length).toBeGreaterThan(0);
    expect(cards.length).toBeLessThan(40);
    expect(screen.getByText('DEMO-1')).toBeInTheDocument();
    expect(screen.queryByText('DEMO-400')).toBeNull();
    expect(container.querySelector('[data-virtualized="true"]')).not.toBeNull();
  });

  it('mounts the cards at the new offset on scroll', () => {
    const { container } = render(
      <ColumnCardList issues={makeIssues(400)} renderCard={renderCard} />,
    );
    const scroller = container.querySelector('[data-virtualized="true"]') as HTMLElement;
    scroller.scrollTop = 399 * 120;
    fireEvent.scroll(scroller);
    expect(screen.getByText('DEMO-400')).toBeInTheDocument();
    expect(screen.queryByText('DEMO-1')).toBeNull();
  });

  it('keeps the dragged card mounted after scrolling away from it', () => {
    const issues = makeIssues(400);
    const { container } = render(
      <ColumnCardList issues={issues} draggingId="DEMO-2" renderCard={renderCard} />,
    );
    const scroller = container.querySelector('[data-virtualized="true"]') as HTMLElement;
    scroller.scrollTop = 200 * 120;
    fireEvent.scroll(scroller);
    expect(screen.getByText('DEMO-2')).toBeInTheDocument();
    expect(screen.getByText('DEMO-200')).toBeInTheDocument();
  });
});
