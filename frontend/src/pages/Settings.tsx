import { useQuery } from '@tanstack/react-query';
import { fetchConfig } from '../api/client';
import { useI18n } from '../i18n/I18nProvider';
import { Badge } from '../components/ui/badge';

export default function Settings() {
  const { data, isLoading } = useQuery({ queryKey: ['config'], queryFn: fetchConfig });
  const { t } = useI18n();

  if (isLoading) return <div className="text-muted-foreground">{t('dashboard.loading')}</div>;
  if (!data) return null;

  const boolBadge = (v: boolean) => (
    <Badge className={v ? 'bg-green-100 text-green-800' : 'bg-gray-100 text-gray-800'}>
      {v ? t('settings.enabled') : t('settings.disabled')}
    </Badge>
  );

  return (
    <div className="space-y-6">
      <h1 className="text-2xl font-bold">{t('settings.title')}</h1>

      <div className="rounded-lg border">
        <div className="grid grid-cols-1 divide-y sm:grid-cols-2 sm:divide-y-0 sm:divide-x">
          <div className="space-y-3 p-6">
            <div className="flex justify-between">
              <span className="text-muted-foreground">{t('settings.mode')}</span>
              <span className="font-medium">{data.mode}</span>
            </div>
            <div className="flex justify-between">
              <span className="text-muted-foreground">{t('settings.nodeId')}</span>
              <span className="font-mono font-medium">{data.node_id}</span>
            </div>
            <div className="flex justify-between">
              <span className="text-muted-foreground">{t('settings.grpcAddr')}</span>
              <span className="font-mono text-sm">{data.grpc_addr}</span>
            </div>
            <div className="flex justify-between">
              <span className="text-muted-foreground">{t('settings.httpAddr')}</span>
              <span className="font-mono text-sm">{data.http_addr}</span>
            </div>
            <div className="flex justify-between">
              <span className="text-muted-foreground">{t('settings.raftHttpAddr')}</span>
              <span className="font-mono text-sm">{data.raft_http_addr}</span>
            </div>
            <div className="flex justify-between">
              <span className="text-muted-foreground">{t('settings.metricsAddr')}</span>
              <span className="font-mono text-sm">{data.metrics_addr}</span>
            </div>
            <div className="flex justify-between">
              <span className="text-muted-foreground">{t('settings.dataDir')}</span>
              <span className="font-mono text-sm">{data.data_dir}</span>
            </div>
          </div>

          <div className="space-y-3 p-6">
            <div className="flex justify-between">
              <span className="text-muted-foreground">{t('settings.maxKeys')}</span>
              <span className="font-medium">{data.max_keys || t('settings.unlimited')}</span>
            </div>
            <div className="flex justify-between">
              <span className="text-muted-foreground">{t('settings.maxValueSize')}</span>
              <span className="font-medium">{data.max_value_size ? `${data.max_value_size} B` : t('settings.unlimited')}</span>
            </div>
            <div className="flex justify-between">
              <span className="text-muted-foreground">{t('settings.maxQPS')}</span>
              <span className="font-medium">{data.max_qps || t('settings.unlimited')}</span>
            </div>
            <div className="flex justify-between">
              <span className="text-muted-foreground">{t('settings.evictionPolicy')}</span>
              <span className="font-medium">{data.eviction_policy}</span>
            </div>
            <div className="flex justify-between">
              <span className="text-muted-foreground">{t('settings.dumpFormat')}</span>
              <span className="font-medium">{data.dump_format}</span>
            </div>
            <div className="flex justify-between">
              <span className="text-muted-foreground">{t('settings.snapshotThreshold')}</span>
              <span className="font-medium">{data.snapshot_threshold}</span>
            </div>
            <div className="flex justify-between">
              <span className="text-muted-foreground">{t('settings.heartbeatElection')}</span>
              <span className="font-medium">{data.heartbeat_ms}ms / {data.election_ms}ms</span>
            </div>
          </div>
        </div>

        <div className="border-t p-6">
          <div className="flex flex-wrap gap-4">
            <div className="flex items-center gap-2">
              <span className="text-sm text-muted-foreground">{t('settings.auth')}:</span>
              {boolBadge(data.auth_enabled)}
            </div>
            <div className="flex items-center gap-2">
              <span className="text-sm text-muted-foreground">{t('settings.tls')}:</span>
              {boolBadge(data.tls_enabled)}
            </div>
            <div className="flex items-center gap-2">
              <span className="text-sm text-muted-foreground">{t('settings.hotReload')}:</span>
              {boolBadge(data.hot_reload)}
            </div>
            <div className="flex items-center gap-2">
              <span className="text-sm text-muted-foreground">{t('settings.snapshots')}:</span>
              {boolBadge(data.snapshot_enabled)}
            </div>
            <div className="flex items-center gap-2">
              <span className="text-sm text-muted-foreground">{t('settings.loadOnStartup')}:</span>
              {boolBadge(data.load_on_startup)}
            </div>
            <div className="flex items-center gap-2">
              <span className="text-sm text-muted-foreground">{t('settings.dumpOnShutdown')}:</span>
              {boolBadge(data.dump_on_shutdown)}
            </div>
          </div>
        </div>
      </div>
    </div>
  );
}
