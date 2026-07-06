import {
  Bot,
  LayoutDashboard,
  Logs,
  type LucideProps,
  Settings,
  TimerReset,
  Workflow,
} from 'lucide-react';

function iconProps(className?: string): LucideProps {
  return {
    className: className ?? 'h-4 w-4',
    strokeWidth: 1.8,
  };
}

export function DashboardIcon({ className }: { className?: string }) {
  return <LayoutDashboard {...iconProps(className)} />;
}

export function TimelineIcon({ className }: { className?: string }) {
  return <TimerReset {...iconProps(className)} />;
}

export function LogsIcon({ className }: { className?: string }) {
  return <Logs {...iconProps(className)} />;
}

export function AgentsIcon({ className }: { className?: string }) {
  return <Bot {...iconProps(className)} />;
}

export function AutomationsIcon({ className }: { className?: string }) {
  return <Workflow {...iconProps(className)} />;
}

export function SettingsIcon({ className }: { className?: string }) {
  return <Settings {...iconProps(className)} />;
}
