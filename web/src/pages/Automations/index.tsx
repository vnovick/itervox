import type { KeyboardEvent } from 'react';
import { useSearchParams } from 'react-router';
import PageMeta from '../../components/common/PageMeta';
import { Card } from '../../components/ui/Card/Card';
import { AutomationsCard } from '../Settings/AutomationsCard';
import { useSettingsPageData } from '../Settings/useSettingsPageData';
import { useUIStore, type AutomationsTab } from '../../store/uiStore';
import AutomationsActivityTab from './AutomationsActivityTab';

const AUTOMATIONS_TABS: Array<{ id: AutomationsTab; label: string }> = [
  { id: 'configure', label: 'Configure' },
  { id: 'activity', label: 'Activity' },
];

export default function Automations() {
  const {
    automations,
    automationProfileOptions,
    trackerStateOptions,
    automationLabelOptions,
    setAutomations,
    setAutomationsTyped,
  } = useSettingsPageData();
  const tab = useUIStore((s) => s.automationsTab);
  const setTab = useUIStore((s) => s.setAutomationsTab);
  const [searchParams] = useSearchParams();
  const focusAutomationId = searchParams.get('openAutomation') ?? undefined;

  return (
    <>
      <PageMeta
        title="Itervox | Automations"
        description="Itervox automations — cron and event-driven helpers that will evolve into canvas"
      />
      <div className="w-full max-w-none space-y-8">
        <div>
          <h1 className="text-theme-text text-2xl font-bold tracking-tight">Automations</h1>
          <p className="text-theme-muted mt-1 text-sm">
            Configure cron and event-driven helper runs. This page is the stepping stone toward the
            future Canvas workflow surface.
          </p>
        </div>

        <AutomationsTabs current={tab} onChange={setTab} />

        <section
          id="automations-panel-configure"
          role="tabpanel"
          aria-labelledby="automations-tab-configure"
          hidden={tab !== 'configure'}
        >
          <div className="space-y-8">
            <Card variant="elevated" className="space-y-2">
              <p className="text-theme-text text-sm font-medium">Automation scope</p>
              <p className="text-theme-muted text-sm leading-relaxed">
                Use this page for practical automations today: scheduled QA checks, backlog review,
                and helper agents that react to input-required events. The broader visual workflow
                canvas will build on top of this surface later.
              </p>
            </Card>

            <AutomationsCard
              automations={automations}
              availableProfiles={automationProfileOptions}
              availableStates={trackerStateOptions}
              availableLabels={automationLabelOptions}
              onSave={setAutomations}
              onSaveTyped={setAutomationsTyped}
              focusAutomationId={focusAutomationId}
            />
          </div>
        </section>

        <section
          id="automations-panel-activity"
          role="tabpanel"
          aria-labelledby="automations-tab-activity"
          hidden={tab !== 'activity'}
        >
          <AutomationsActivityTab />
        </section>
      </div>
    </>
  );
}

interface AutomationsTabsProps {
  current: AutomationsTab;
  onChange: (tab: AutomationsTab) => void;
}

function AutomationsTabs({ current, onChange }: AutomationsTabsProps) {
  const focusTab = (id: AutomationsTab) => {
    onChange(id);
    document.getElementById(`automations-tab-${id}`)?.focus();
  };

  const handleKeyDown = (event: KeyboardEvent<HTMLButtonElement>, id: AutomationsTab) => {
    const index = AUTOMATIONS_TABS.findIndex((tab) => tab.id === id);
    if (index === -1) return;
    let next: AutomationsTab;
    switch (event.key) {
      case 'ArrowLeft':
        next = AUTOMATIONS_TABS[(index - 1 + AUTOMATIONS_TABS.length) % AUTOMATIONS_TABS.length].id;
        break;
      case 'ArrowRight':
        next = AUTOMATIONS_TABS[(index + 1) % AUTOMATIONS_TABS.length].id;
        break;
      case 'Home':
        next = AUTOMATIONS_TABS[0].id;
        break;
      case 'End':
        next = AUTOMATIONS_TABS[AUTOMATIONS_TABS.length - 1].id;
        break;
      default:
        return;
    }
    event.preventDefault();
    focusTab(next);
  };

  return (
    <div
      role="tablist"
      aria-label="Automations sections"
      data-testid="automations-tablist"
      className="border-theme-line flex gap-1 border-b"
    >
      {AUTOMATIONS_TABS.map((t) => {
        const active = current === t.id;
        return (
          <button
            key={t.id}
            type="button"
            role="tab"
            aria-selected={active}
            id={`automations-tab-${t.id}`}
            data-testid={`automations-tab-${t.id}`}
            aria-controls={`automations-panel-${t.id}`}
            tabIndex={active ? 0 : -1}
            onClick={() => {
              onChange(t.id);
            }}
            onKeyDown={(event) => {
              handleKeyDown(event, t.id);
            }}
            className={
              'border-b-2 px-3 py-2 text-sm font-medium transition-colors ' +
              (active
                ? 'border-theme-accent text-theme-text'
                : 'text-theme-muted hover:text-theme-text border-transparent')
            }
          >
            {t.label}
          </button>
        );
      })}
    </div>
  );
}
