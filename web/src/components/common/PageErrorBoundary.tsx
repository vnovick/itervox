import { Component, type ErrorInfo, type ReactNode } from 'react';
import { reportClientError } from '../../lib/clientErrorReporter';
import { plainButtonClass } from '../ui/button/buttonRecipe';
import { StatusCard } from './StatusCard';

interface Props {
  children: ReactNode;
}

interface State {
  error: Error | null;
}

/**
 * Per-page error boundary that catches rendering errors within a single route
 * and displays a recovery UI with a link back to the Dashboard.
 * Unlike the root ErrorBoundary, this keeps the app shell (sidebar, header)
 * intact so the user can navigate away without a full page reload.
 */
export class PageErrorBoundary extends Component<Props, State> {
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
      console.error('[Itervox] Page render error', error, info.componentStack);
    }
  }

  render() {
    const { error } = this.state;
    if (!error) return this.props.children;

    return (
      <div className="flex justify-center py-10">
        <StatusCard eyebrow="Page error" title="This page crashed" level={2}>
          <p>An unexpected error occurred while rendering this page.</p>
          <pre className="code-block max-h-48 text-left">{error.message}</pre>
          <a href="/" className={plainButtonClass('primary', 'lg')}>
            Go to Dashboard
          </a>
        </StatusCard>
      </div>
    );
  }
}
