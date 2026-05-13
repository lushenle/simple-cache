import { useParams, useNavigate } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';
import { fetchKeyValue } from '../api/client';
import { useI18n } from '../i18n/I18nProvider';
import { Button } from '../components/ui/button';
import { ArrowLeft } from 'lucide-react';

export default function KeyDetail() {
  const { key } = useParams<{ key: string }>();
  const navigate = useNavigate();
  const decodedKey = decodeURIComponent(key || '');
  const { t } = useI18n();

  const { data, isLoading, error } = useQuery({
    queryKey: ['key', decodedKey],
    queryFn: () => fetchKeyValue(decodedKey),
    enabled: !!decodedKey,
  });

  if (isLoading) return <div className="text-muted-foreground">{t('dashboard.loading')}</div>;

  return (
    <div className="space-y-6">
      <div className="flex items-center gap-4">
        <Button variant="ghost" size="sm" onClick={() => navigate('/cache')}>
          <ArrowLeft className="h-4 w-4" /> {t('keyDetail.back')}
        </Button>
        <h1 className="text-2xl font-bold font-mono">{decodedKey}</h1>
      </div>

      {error && <div className="text-destructive">{t('dashboard.failed')}</div>}

      {data && (
        <div className="space-y-4">
          <div className="grid grid-cols-2 gap-4">
            <div className="rounded-lg border p-4">
              <div className="text-sm text-muted-foreground">{t('keyDetail.found')}</div>
              <div className="text-lg font-bold">{data.found ? t('keyDetail.yes') : t('keyDetail.no')}</div>
            </div>
          </div>

          {data.found && (
            <div className="rounded-lg border p-4">
              <h3 className="mb-2 text-sm font-medium">{t('cache.value')}</h3>
              <pre className="max-h-96 overflow-auto whitespace-pre-wrap break-all rounded bg-muted p-4 text-sm">
                {typeof data.value === 'object'
                  ? JSON.stringify(data.value, null, 2)
                  : String(data.value ?? '(empty)')}
              </pre>
            </div>
          )}
        </div>
      )}
    </div>
  );
}
