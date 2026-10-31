import { Component, type ErrorInfo, type ReactNode } from 'react';
import { reportClientError } from '../../lib/clientErrorReporter';
import { plainButtonClass } from '../ui/button/buttonRecipe';
import { StatusCard, StatusScreen } from './StatusCard';

interface Props {
  children: ReactNode;
}

interface State {
  error: Error | null;
}

/**
 * Catches render errors anywhere in the component tree and shows a recovery UI
 * instead of a blank screen. Wrap at the app root so crashes are always caught.
 */
export class ErrorBoundary extends Component<Props, State> {
  state: State = { error: null };

  static getDerivedStateFromError(error: Error): State {
    return { error };
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    // CORE-048: report in production too, so the operator sees the crash.
    reportClientError({
      kind: 'render',
      message: error.message,
      stack: `${error.stack ?? ''}${info.componentStack ?? ''}`,
    });
    if (import.meta.env.DEV) {
      console.error('[Itervox] Unhandled render error', error, info.componentStack);
    }
  }

  render() {
    const { error } = this.state;
    if (!error) return this.props.children;

    return (
      <StatusScreen>
        <StatusCard eyebrow="Dashboard error" title="Something went wrong">
          <p>The dashboard encountered an unexpected error. Try refreshing the page.</p>
          <pre className="code-block max-h-48 text-left">{error.message}</pre>
          <button
            type="button"
            onClick={() => {
              window.location.reload();
            }}
            className={plainButtonClass('primary', 'lg')}
          >
            Reload
          </button>
        </StatusCard>
      </StatusScreen>
    );
  }
}
