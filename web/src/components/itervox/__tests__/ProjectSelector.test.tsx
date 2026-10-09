import { render, screen } from '@testing-library/react';
import { describe, it, expect, vi } from 'vitest';
import { ProjectSelector } from '../ProjectSelector';

vi.mock('../../../store/itervoxStore', () => ({
  useItervoxStore: vi.fn(),
}));

import { useItervoxStore } from '../../../store/itervoxStore';

const mockStore = useItervoxStore as unknown as ReturnType<typeof vi.fn>;

describe('ProjectSelector', () => {
  it('renders nothing when trackerKind is not set', () => {
    mockStore.mockImplementation((selector: (s: unknown) => unknown) =>
      selector({ snapshot: { trackerKind: undefined, activeProjectFilter: [] } }),
    );
    const { container } = render(<ProjectSelector />);
    expect(container.firstChild).toBeNull();
  });

  it('renders nothing when snapshot is null', () => {
    mockStore.mockImplementation((selector: (s: unknown) => unknown) =>
      selector({ snapshot: null }),
    );
    const { container } = render(<ProjectSelector />);
    expect(container.firstChild).toBeNull();
  });

  it('renders when trackerKind is linear', () => {
    mockStore.mockImplementation((selector: (s: unknown) => unknown) =>
      selector({ snapshot: { trackerKind: 'linear', activeProjectFilter: [] } }),
    );
    render(<ProjectSelector />);
    expect(screen.getByTestId('project-selector')).toBeInTheDocument();
  });

  it('renders when trackerKind is github', () => {
    mockStore.mockImplementation((selector: (s: unknown) => unknown) =>
      selector({ snapshot: { trackerKind: 'github', activeProjectFilter: [] } }),
    );
    render(<ProjectSelector />);
    expect(screen.getByTestId('project-selector')).toBeInTheDocument();
  });

  it('labels the local file tracker as local files, not GitHub (#85)', () => {
    mockStore.mockImplementation((selector: (s: unknown) => unknown) =>
      selector({ snapshot: { trackerKind: 'local', activeProjectFilter: [] } }),
    );
    render(<ProjectSelector />);
    expect(screen.getByTestId('tracker-badge')).toHaveTextContent('Local files');
    expect(screen.queryByText(/GitHub/)).not.toBeInTheDocument();
  });

  it('displays the tracker source label', () => {
    mockStore.mockImplementation((selector: (s: unknown) => unknown) =>
      selector({ snapshot: { trackerKind: 'linear', activeProjectFilter: [] } }),
    );
    render(<ProjectSelector />);
    expect(screen.getByText(/linear/i)).toBeInTheDocument();
  });

  it('shows active project filters when provided', () => {
    mockStore.mockImplementation((selector: (s: unknown) => unknown) =>
      selector({
        snapshot: {
          trackerKind: 'linear',
          activeProjectFilter: ['Project A', 'Project B'],
        },
      }),
    );
    render(<ProjectSelector />);
    expect(screen.getByText('Project A')).toBeInTheDocument();
    expect(screen.getByText('Project B')).toBeInTheDocument();
  });
});
