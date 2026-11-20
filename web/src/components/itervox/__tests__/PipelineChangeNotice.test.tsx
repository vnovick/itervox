import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it } from 'vitest';
import { PipelineChangeNotice } from '../PipelineChangeNotice';
import { useItervoxStore } from '../../../store/itervoxStore';
import { useUIStore } from '../../../store/uiStore';
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

  it('says why the push needs a person, with file and line', () => {
    useItervoxStore.setState({
      snapshot: makeSnapshot({
        inputRequired: [
          makeInputRequiredRow({
            identifier: 'ENG-1',
            guardrailApproval: true,
            guardrailLanePaths: ['.github/workflows/ci.yml'],
            guardrailLanePathsTotal: 1,
            guardrailLaneReasons: [
              {
                kind: 'finding',
                path: '.github/workflows/ci.yml',
                line: 10,
                rule: 'ci_continue_on_error',
                message: 'failure ignored in a CI workflow',
                text: 'continue-on-error: true',
              },
              {
                kind: 'lane_rule',
                path: '.github/workflows/deploy.yml',
                lane: 'human',
                rule: '.github/workflows/**',
              },
              { kind: 'tool_behaviour', path: 'Makefile', message: 'the test target changed' },
            ],
            guardrailLaneReasonsTotal: 12,
          }),
        ],
      }),
    });
    render(<PipelineChangeNotice identifier="ENG-1" />);
    const why = screen.getByRole('list', { name: 'Why this needs a person' });
    const items = within(why).getAllByRole('listitem');
    expect(items).toHaveLength(3);
    expect(items[0]).toHaveTextContent(
      '.github/workflows/ci.yml:10 adds continue-on-error: true — failure ignored in a CI workflow',
    );
    expect(items[1]).toHaveTextContent(
      '.github/workflows/deploy.yml matched .github/workflows/** → human',
    );
    expect(items[2]).toHaveTextContent('Makefile: the test target changed');
    expect(screen.getByText('and 9 more')).toBeInTheDocument();
  });

  it('links straight to the issue Lanes view when the daemon serves it', async () => {
    useItervoxStore.setState({
      selectedIdentifier: null,
      snapshot: makeSnapshot({
        capabilities: ['run_context'],
        inputRequired: [
          makeInputRequiredRow({ identifier: 'ENG-1', guardrailLanePaths: ['Makefile'] }),
        ],
      }),
    });
    useUIStore.setState({ issueLanesFor: null });
    render(<PipelineChangeNotice identifier="ENG-1" />);
    await userEvent.click(screen.getByRole('button', { name: 'Open the Lanes view' }));
    expect(useItervoxStore.getState().selectedIdentifier).toBe('ENG-1');
    expect(useUIStore.getState().issueLanesFor).toBe('ENG-1');
  });

  it('offers no Lanes link without the run-context capability', () => {
    useItervoxStore.setState({
      snapshot: makeSnapshot({
        capabilities: [],
        inputRequired: [
          makeInputRequiredRow({ identifier: 'ENG-1', guardrailLanePaths: ['Makefile'] }),
        ],
      }),
    });
    render(<PipelineChangeNotice identifier="ENG-1" />);
    expect(screen.queryByRole('button', { name: 'Open the Lanes view' })).toBeNull();
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
