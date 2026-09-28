import { Component, type ErrorInfo, type ReactNode } from 'react';
import { reportClientError } from '../../lib/clientErrorReporter';

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
      <div className="flex min-h-screen flex-col items-center justify-center gap-4 p-8 text-center">
        <h1 className="text-2xl font-semibold text-red-600 dark:text-red-400">
          Something went wrong
        </h1>
        <p className="max-w-md text-sm text-gray-600 dark:text-gray-400">
          The dashboard encountered an unexpected error. Try refreshing the page.
        </p>
        <pre className="max-w-lg overflow-auto rounded bg-gray-100 p-4 text-left text-xs text-gray-800 dark:bg-gray-800 dark:text-gray-200">
          {error.message}
        </pre>
        <button
          onClick={() => {
            window.location.reload();
          }}
          className="rounded bg-blue-600 px-4 py-2 text-sm text-white hover:bg-blue-700"
        >
          Reload
        </button>
      </div>
    );
  }
}
