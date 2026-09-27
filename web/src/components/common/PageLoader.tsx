/**
 * Route-level Suspense fallback. CORE-089 — announced as a status with
 * screen-reader text instead of an unlabelled spinner.
 */
export function PageLoader() {
  return (
    <div role="status" className="flex h-64 items-center justify-center">
      <div
        aria-hidden="true"
        className="h-6 w-6 animate-spin rounded-full border-2 border-current border-t-transparent"
      />
      <span className="sr-only">Loading page…</span>
    </div>
  );
}
