import GridShape from '../../components/common/GridShape';
import { Link } from 'react-router';
import PageMeta from '../../components/common/PageMeta';

export default function NotFound() {
  return (
    <>
      <PageMeta
        title="404 Not Found | Itervox"
        description="The page you are looking for could not be found."
      />
      <div className="relative z-1 flex min-h-[60dvh] flex-col items-center justify-center overflow-hidden p-6">
        <GridShape />
        <div className="mx-auto w-full max-w-[242px] text-center sm:max-w-[472px]">
          <h1 className="text-title-md xl:text-title-2xl text-theme-text mb-8 font-bold">ERROR</h1>

          <img src="/images/error/404.svg" alt="404" className="dark:hidden" />
          <img src="/images/error/404-dark.svg" alt="404" className="hidden dark:block" />

          <p className="text-theme-text-secondary mt-10 mb-6 text-base sm:text-lg">
            We can’t seem to find the page you are looking for!
          </p>

          <Link
            to="/"
            className="border-theme-line bg-theme-bg-elevated text-theme-text hover:bg-theme-panel-strong inline-flex items-center justify-center rounded-lg border px-5 py-3.5 text-sm font-medium"
          >
            Back to Home Page
          </Link>
        </div>
        {/* <!-- Footer --> */}
        <p className="text-theme-muted mt-10 text-center text-sm">
          &copy; {new Date().getFullYear()} itervox contributors
        </p>
      </div>
    </>
  );
}
