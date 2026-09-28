import { Component, type ErrorInfo, type ReactNode } from 'react';
import { reportClientError } from '../../lib/clientErrorReporter';

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
      <div className="flex flex-col items-center justify-center gap-4 py-20 text-center">
        <h2 className="text-theme-danger-text text-xl font-semibold">This page crashed</h2>
        <p className="text-theme-muted max-w-md text-sm">
          An unexpected error occurred while rendering this page.
        </p>
        <pre className="border-theme-line bg-theme-bg-soft text-theme-text-secondary max-w-lg overflow-auto rounded-lg border p-4 text-left text-xs">
          {error.message}
        </pre>
        <a
          href="/"
          className="bg-theme-accent rounded-lg px-4 py-2 text-sm font-medium text-white transition-opacity hover:opacity-90"
        >
          Go to Dashboard
        </a>
      </div>
    );
  }
}
