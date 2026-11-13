import { Link } from 'react-router';
import PageMeta from '../../components/common/PageMeta';
import { StatusCard } from '../../components/common/StatusCard';
import { plainButtonClass } from '../../components/ui/button/buttonRecipe';

export default function NotFound() {
  return (
    <>
      <PageMeta
        title="404 Not Found | Itervox"
        description="The page you are looking for could not be found."
      />
      <div className="flex min-h-[60dvh] items-center justify-center py-6">
        <StatusCard eyebrow="404" title={<span className="font-mono tracking-wider">ERROR</span>}>
          <p>We can’t seem to find the page you are looking for!</p>
          <Link to="/" className={plainButtonClass('primary', 'lg')}>
            Back to Home Page
          </Link>
        </StatusCard>
      </div>
    </>
  );
}
