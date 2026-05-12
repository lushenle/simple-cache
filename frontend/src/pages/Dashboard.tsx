import { useQuery } from '@tanstack/react-query';
import { fetchStatus, fetchMetricsSummary } from '../api/client';
import { useI18n } from '../i18n/I18nProvider';
import { formatBytes } from '../lib/utils';
import { Server, Activity, Database, Cpu } from 'lucide-react';

function StatCard({ title, value, subtitle, icon: Icon }: {
  title: string;
  value: string;
  subtitle?: string;
  icon: React.ComponentType<{ className?: string }>;
}) {
  return (
    <div className="rounded-lg border bg-card p-4">
      <div className="flex items-center justify-between">
        <span className="text-sm text-muted-foreground">{title}</span>
        <Icon className="h-4 w-4 text-muted-foreground" />
      </div>
      <div className="mt-2 text-2xl font-bold">{value}</div>
      {subtitle && <div className="mt-1 text-xs text-muted-foreground">{subtitle}</div>}
    </div>
  );
}

export default function Dashboard() {
  const status = useQuery({ queryKey: ['status'], queryFn: fetchStatus, refetchInterval: 5000 });
  const metrics = useQuery({ queryKey: ['metrics'], queryFn: fetchMetricsSummary, refetchInterval: 5000 });
  const { t } = useI18n();

  if (status.isLoading) return <div className="text-muted-foreground">{t('dashboard.loading')}</div>;
  if (status.error) return (
    <div className="rounded-lg border border-destructive/50 bg-destructive/10 p-6">
      <p className="font-semibold text-destructive">{t('dashboard.failed')}</p>
      <p className="mt-1 text-sm text-muted-foreground">
        {(status.error as Error).message || String(status.error)}
      </p>
    </div>
  );
  if (!status.data) return null;

  const s = status.data;
  const m = metrics.data;

  const totalRequests = m
    ? Object.values(m.requests_by_op).reduce((sum, op) => sum + op.success + op.error, 0)
    : 0;

  const modeLabel = s.mode === 'distributed' ? t('common.distributed') : t('common.single');

  return (
    <div className="space-y-6">
      <h1 className="text-2xl font-bold">{t('dashboard.title')}</h1>

      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <StatCard
          title={t('dashboard.keys')}
          value={s.keys_total.toLocaleString()}
          subtitle={`${s.cache_stats.eviction_policy} ${t('dashboard.eviction')}`}
          icon={Database}
        />
        <StatCard
          title={t('dashboard.memory')}
          value={formatBytes(s.memory_alloc)}
          subtitle={`${formatBytes(s.memory_total)} ${t('dashboard.total')}`}
          icon={Cpu}
        />
        <StatCard
          title={t('dashboard.requests')}
          value={totalRequests.toLocaleString()}
          subtitle={t('dashboard.totalProcessed')}
          icon={Activity}
        />
        <StatCard
          title={t('dashboard.cluster')}
          value={s.mode === 'distributed' ? `${m?.peers_total ?? 0} ${t('dashboard.peers')}` : modeLabel}
          subtitle={s.role}
          icon={Server}
        />
      </div>

      {m && (
        <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
          <div className="rounded-lg border bg-card p-4">
            <h3 className="mb-3 text-sm font-medium">{t('dashboard.raftState')}</h3>
            <div className="space-y-2 text-sm">
              <div className="flex justify-between">
                <span className="text-muted-foreground">{t('dashboard.role')}</span>
                <span className="font-medium">{m.raft_role}</span>
              </div>
              <div className="flex justify-between">
                <span className="text-muted-foreground">{t('dashboard.commitIndex')}</span>
                <span className="font-medium">{m.raft_commit_index}</span>
              </div>
              <div className="flex justify-between">
                <span className="text-muted-foreground">{t('dashboard.lastApplied')}</span>
                <span className="font-medium">{m.raft_last_applied}</span>
              </div>
              <div className="flex justify-between">
                <span className="text-muted-foreground">{t('dashboard.pendingEntries')}</span>
                <span className="font-medium">{m.raft_pending_entries}</span>
              </div>
            </div>
          </div>

          <div className="rounded-lg border bg-card p-4">
            <h3 className="mb-3 text-sm font-medium">{t('dashboard.requestsByOp')}</h3>
            <div className="space-y-2 text-sm">
              {Object.entries(m.requests_by_op).map(([op, counts]) => (
                <div key={op} className="flex items-center justify-between">
                  <span className="text-muted-foreground">{op}</span>
                  <div className="flex gap-2">
                    <span className="rounded bg-green-100 px-2 py-0.5 text-xs text-green-800 dark:bg-green-900 dark:text-green-100">
                      {counts.success} ok
                    </span>
                    {counts.error > 0 && (
                      <span className="rounded bg-red-100 px-2 py-0.5 text-xs text-red-800 dark:bg-red-900 dark:text-red-100">
                        {counts.error} err
                      </span>
                    )}
                  </div>
                </div>
              ))}
            </div>
          </div>
        </div>
      )}
    </div>
  );
}
