import React, { Suspense } from 'react';
import { proseClass } from '../../utils/format';

const LazyMarkdown = React.lazy(() =>
  Promise.all([import('react-markdown'), import('remark-gfm')]).then(
    ([{ default: ReactMarkdown }, { default: remarkGfm }]) => ({
      default: (props: { children: string }) => (
        <ReactMarkdown remarkPlugins={[remarkGfm]}>{props.children}</ReactMarkdown>
      ),
    }),
  ),
);

/**
 * Renders Markdown content inside a `proseClass`-styled wrapper, deferring
 * `react-markdown` + `remark-gfm` to a lazy chunk so the dashboard's main
 * bundle stays small. Extracted from `IssueDetailSlide.tsx` (T-20) which
 * previously open-coded the same Suspense+proseClass+LazyMarkdown trio in
 * four places — issue description, comment body, error sections.
 */
export default function MarkdownPanel({ children }: { children: string }) {
  return (
    <div className={proseClass}>
      <Suspense fallback={<div className="animate-pulse">Loading...</div>}>
        <LazyMarkdown>{children}</LazyMarkdown>
      </Suspense>
    </div>
  );
}
