// Repo Analysis (#200): repository-level signals in one page — the skills
// inventory (Capabilities), runtime usage and cost, prompt versions (#186)
// and the `itervox doctor` readiness checks. Sub-tabs on wide screens, all
// sections stacked on small ones; the location hash selects the tab
// (/repo#capabilities, #usage, #prompts, #readiness).

import type { KeyboardEvent, ReactNode } from 'react';
import { useLocation, useNavigate } from 'react-router';
import PageMeta from '../../components/common/PageMeta';
import { SkillsCard } from './capabilities/SkillsCard';
import { PromptVersionsSection } from './prompts/PromptVersionsSection';
import { ReadinessSection } from './readiness/ReadinessSection';
import { REPO_SECTIONS, sectionFromHash, type RepoSectionKey } from './repoSections';
import { UsageSection } from './usage/UsageSection';

const SECTION_BODY: Record<RepoSectionKey, () => ReactNode> = {
  capabilities: () => <SkillsCard />,
  usage: () => <UsageSection />,
  prompts: () => <PromptVersionsSection />,
  readiness: () => <ReadinessSection />,
};

export default function RepoAnalysis() {
  const location = useLocation();
  const navigate = useNavigate();
  const active = sectionFromHash(location.hash);

  const select = (key: RepoSectionKey) => {
    void navigate({ hash: key }, { replace: true });
  };

  const onKeyDown = (e: KeyboardEvent<HTMLButtonElement>, index: number) => {
    const n = REPO_SECTIONS.length;
    const to =
      e.key === 'ArrowRight' ? (index + 1) % n : e.key === 'ArrowLeft' ? (index - 1 + n) % n : -1;
    if (to < 0) return;
    e.preventDefault();
    select(REPO_SECTIONS[to].key);
    document.getElementById(`repo-tab-${REPO_SECTIONS[to].key}`)?.focus();
  };

  return (
    <>
      <PageMeta
        title="Itervox | Repo Analysis"
        description="Itervox repo analysis — capabilities, runtime usage and cost, prompt versions and agent readiness"
      />
      <div className="w-full max-w-5xl space-y-6">
        <div>
          <h1 className="text-theme-text text-2xl font-bold tracking-tight">Repo Analysis</h1>
          <p className="text-theme-muted mt-1 text-sm">
            What your agents load, what it costs, which prompt version ran, and whether the
            repository is ready for agents.
          </p>
        </div>
        <div
          role="tablist"
          aria-label="Repo Analysis sections"
          className="border-theme-line hidden gap-1 border-b md:flex"
        >
          {REPO_SECTIONS.map((s, i) => {
            const selected = s.key === active;
            return (
              <button
                key={s.key}
                id={`repo-tab-${s.key}`}
                type="button"
                role="tab"
                aria-selected={selected}
                aria-controls={s.key}
                tabIndex={selected ? 0 : -1}
                className={`-mb-px border-b-2 px-3 py-2 text-sm font-medium ${
                  selected
                    ? 'border-theme-accent text-theme-text'
                    : 'text-theme-muted hover:text-theme-text border-transparent'
                }`}
                onClick={() => {
                  select(s.key);
                }}
                onKeyDown={(e) => {
                  onKeyDown(e, i);
                }}
              >
                {s.label}
              </button>
            );
          })}
        </div>
        {REPO_SECTIONS.map((s) => (
          <section
            key={s.key}
            id={s.key}
            aria-labelledby={`repo-section-${s.key}`}
            data-active={s.key === active}
            className={`space-y-3 ${s.key === active ? '' : 'md:hidden'}`}
          >
            <h2
              id={`repo-section-${s.key}`}
              className="text-xs font-semibold tracking-widest uppercase md:sr-only"
            >
              {s.label}
            </h2>
            {SECTION_BODY[s.key]()}
          </section>
        ))}
      </div>
    </>
  );
}
