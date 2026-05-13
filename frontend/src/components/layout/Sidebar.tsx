import { NavLink } from 'react-router-dom';
import {
  LayoutDashboard,
  Server,
  Database,
  Radio,
  Settings,
  Wrench,
} from 'lucide-react';
import { cn } from '../../lib/utils';

const navItems = [
  { to: '/', icon: LayoutDashboard, label: 'Dashboard' },
  { to: '/cluster', icon: Server, label: 'Cluster' },
  { to: '/cache', icon: Database, label: 'Cache Browser' },
  { to: '/subscriptions', icon: Radio, label: 'Subscriptions' },
  { to: '/operations', icon: Wrench, label: 'Operations' },
  { to: '/settings', icon: Settings, label: 'Settings' },
];

export default function Sidebar() {
  return (
    <aside className="flex w-56 flex-col border-r bg-card">
      <div className="flex h-14 items-center gap-2 border-b px-4">
        <Database className="h-5 w-5 text-primary" />
        <span className="font-semibold text-sm">Simple Cache</span>
      </div>
      <nav className="flex-1 space-y-1 p-3">
        {navItems.map(({ to, icon: Icon, label }) => (
          <NavLink
            key={to}
            to={to}
            end={to === '/'}
            className={({ isActive }) =>
              cn(
                'flex items-center gap-3 rounded-md px-3 py-2 text-sm font-medium transition-colors',
                isActive
                  ? 'bg-primary text-primary-foreground'
                  : 'text-muted-foreground hover:bg-accent hover:text-accent-foreground',
              )
            }
          >
            <Icon className="h-4 w-4" />
            {label}
          </NavLink>
        ))}
      </nav>
    </aside>
  );
}
