import { render, screen } from '@testing-library/react';
import { beforeEach, describe, expect, it } from 'vitest';
import { PipelineChangeNotice } from '../PipelineChangeNotice';
import { useItervoxStore } from '../../../store/itervoxStore';
import { makeInputRequiredRow, makeSnapshot } from '../../../test/fixtures/snapshots';

describe('PipelineChangeNotice', () => {
  beforeEach(() => {
    useItervoxStore.setState({ snapshot: makeSnapshot() });
  });

  it('labels a held pipeline push with its files', () => {
    useItervoxStore.setState({
      snapshot: makeSnapshot({
        inputRequired: [
          makeInputRequiredRow({
            identifier: 'ENG-1',
            guardrailApproval: true,
            guardrailLanePaths: ['.github/workflows/ci.yml', 'Makefile'],
            guardrailLanePathsTotal: 2,
          }),
        ],
      }),
    });
    render(<PipelineChangeNotice identifier="ENG-1" />);
    expect(screen.getByText('Pipeline change')).toBeInTheDocument();
    expect(screen.getByText('.github/workflows/ci.yml')).toBeInTheDocument();
    expect(screen.getByText('Makefile')).toBeInTheDocument();
    expect(screen.queryByText(/more$/)).not.toBeInTheDocument();
  });

  it('names at most ten files, then how many more', () => {
    const paths = Array.from({ length: 12 }, (_, i) => `.github/workflows/w${String(i)}.yml`);
    useItervoxStore.setState({
      snapshot: makeSnapshot({
        inputRequired: [
          makeInputRequiredRow({
            identifier: 'ENG-1',
            guardrailLanePaths: paths,
            guardrailLanePathsTotal: 60,
          }),
        ],
      }),
    });
    render(<PipelineChangeNotice identifier="ENG-1" />);
    expect(screen.getAllByRole('listitem')).toHaveLength(10);
    expect(screen.getByText('and 50 more')).toBeInTheDocument();
  });

  it('renders nothing for an ordinary input request or another issue', () => {
    useItervoxStore.setState({
      snapshot: makeSnapshot({
        inputRequired: [
          makeInputRequiredRow({ identifier: 'ENG-1' }),
          makeInputRequiredRow({ identifier: 'ENG-2', guardrailLanePaths: ['Makefile'] }),
        ],
      }),
    });
    const { container } = render(<PipelineChangeNotice identifier="ENG-1" />);
    expect(container).toBeEmptyDOMElement();
  });
});
