import { HelmetProvider, Helmet } from 'react-helmet-async';
import { useAttentionCount } from '../../hooks/useOperatorQueue';
import { withAttentionPrefix } from '../../lib/attentionTitle';

// PageMeta is the single title writer (react-helmet-async). CORE-078: it
// prefixes the attention count — the same useAttentionCount source as the
// inbox and the nav badge — so a background tab shows "(3) Itervox | …".
const PageMeta = ({ title, description }: { title: string; description?: string }) => {
  const attention = useAttentionCount();
  return (
    <Helmet>
      <title>{withAttentionPrefix(title, attention)}</title>
      {description && <meta name="description" content={description} />}
    </Helmet>
  );
};

export const AppWrapper = ({ children }: { children: React.ReactNode }) => (
  <HelmetProvider>{children}</HelmetProvider>
);

export default PageMeta;
