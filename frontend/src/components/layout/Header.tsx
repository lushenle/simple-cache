import { useQuery } from '@tanstack/react-query';
import { useNavigate } from 'react-router-dom';
import { fetchStatus } from '../../api/client';
import { useAuth } from '../../hooks/useAuth';
import { Badge } from '../ui/badge';
import { Button } from '../ui/button';
import { LogOut, Moon, Sun, Languages } from 'lucide-react';
import { useState, useCallback } from 'react';
import { useI18n } from '../../i18n/I18nProvider';

export default function Header() {
  const { data } = useQuery({
    queryKey: ['header-status'],
    queryFn: fetchStatus,
    refetchInterval: 10000,
  });
  const { logout } = useAuth();
  const navigate = useNavigate();
  const { locale, setLocale, t } = useI18n();
  const [dark, setDark] = useState(() => document.documentElement.classList.contains('dark'));

  const toggleDark = useCallback(() => {
    setDark((prev) => {
      const next = !prev;
      document.documentElement.classList.toggle('dark', next);
      localStorage.setItem('theme', next ? 'dark' : 'light');
      return next;
    });
  }, []);

  const roleBadge = (role: string) => {
    const colors: Record<string, string> = {
      leader: 'bg-green-100 text-green-800 dark:bg-green-900 dark:text-green-100',
      follower: 'bg-blue-100 text-blue-800 dark:bg-blue-900 dark:text-blue-100',
      candidate: 'bg-yellow-100 text-yellow-800 dark:bg-yellow-900 dark:text-yellow-100',
      single: 'bg-gray-100 text-gray-800 dark:bg-gray-900 dark:text-gray-100',
    };
    return colors[role] || colors.single;
  };

  return (
    <header className="flex h-14 items-center justify-between border-b bg-card px-6">
      <div className="flex items-center gap-4 text-sm">
        {data && (
          <>
            <span className="text-muted-foreground">
              {data.node_id} ({data.mode})
            </span>
            <Badge className={roleBadge(data.role)}>{data.role}</Badge>
            <span className="text-muted-foreground">{data.keys_total} keys</span>
            <span className="text-muted-foreground">uptime: {data.uptime}</span>
          </>
        )}
      </div>
      <div className="flex items-center gap-2">
        <Button
          variant="ghost"
          size="sm"
          onClick={() => setLocale(locale === 'en' ? 'zh' : 'en')}
          title={t('lang.switch')}
          className="gap-1 text-xs font-medium"
        >
          <Languages className="h-4 w-4" />
          {t('lang.label')}
        </Button>
        <Button variant="ghost" size="icon" onClick={toggleDark}>
          {dark ? <Sun className="h-4 w-4" /> : <Moon className="h-4 w-4" />}
        </Button>
        <Button
          variant="ghost"
          size="icon"
          onClick={() => {
            logout();
            navigate('/login');
          }}
        >
          <LogOut className="h-4 w-4" />
        </Button>
      </div>
    </header>
  );
}
