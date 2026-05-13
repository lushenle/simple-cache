import { useState } from 'react';
import { useMutation, useQuery } from '@tanstack/react-query';
import { dumpCache, loadCache, fetchKeysStats } from '../api/client';
import { useI18n } from '../i18n/I18nProvider';
import { Button } from '../components/ui/button';
import { Input } from '../components/ui/input';
import { Select } from '../components/ui/select';
import { Download, Upload, FileJson } from 'lucide-react';

export default function Operations() {
  const [dumpFormat, setDumpFormat] = useState('binary');
  const [dumpPath, setDumpPath] = useState('');
  const [loadPath, setLoadPath] = useState('');
  const { t } = useI18n();

  const keysStats = useQuery({ queryKey: ['keysStats'], queryFn: fetchKeysStats, refetchInterval: 10000 });

  const dumpMut = useMutation({ mutationFn: () => dumpCache(dumpFormat, dumpPath || undefined) });
  const loadMut = useMutation({ mutationFn: () => loadCache(loadPath || undefined) });

  return (
    <div className="space-y-6">
      <h1 className="text-2xl font-bold">{t('ops.title')}</h1>

      <div className="grid grid-cols-1 gap-6 lg:grid-cols-2">
        <div className="space-y-4 rounded-lg border p-6">
          <div className="flex items-center gap-2">
            <Download className="h-5 w-5" />
            <h2 className="text-lg font-semibold">{t('ops.dumpCache')}</h2>
          </div>
          <p className="text-sm text-muted-foreground">{t('ops.dumpDesc')}</p>
          <div className="space-y-2">
            <label className="text-sm font-medium">{t('ops.format')}</label>
            <Select value={dumpFormat} onChange={setDumpFormat} options={[
              { value: 'binary', label: 'Binary' },
              { value: 'json', label: 'JSON' },
            ]} />
          </div>
          <div className="space-y-2">
            <label className="text-sm font-medium">{t('ops.pathOptional')}</label>
            <Input value={dumpPath} onChange={(e) => setDumpPath(e.target.value)} placeholder={t('ops.autoPath')} />
          </div>
          <Button onClick={() => dumpMut.mutate()} disabled={dumpMut.isPending} className="w-full">
            {dumpMut.isPending ? t('ops.dumping') : t('ops.dumpNow')}
          </Button>
          {dumpMut.data && (
            <div className="rounded bg-green-50 p-3 text-sm dark:bg-green-950">
              <p>Dumped {dumpMut.data.totalKeys} keys to {dumpMut.data.path}</p>
              <p className="text-muted-foreground">Size: {dumpMut.data.fileSize} bytes, Duration: {dumpMut.data.durationMs}ms</p>
            </div>
          )}
          {dumpMut.error && <p className="text-sm text-destructive">{dumpMut.error.message}</p>}
        </div>

        <div className="space-y-4 rounded-lg border p-6">
          <div className="flex items-center gap-2">
            <Upload className="h-5 w-5" />
            <h2 className="text-lg font-semibold">{t('ops.loadCache')}</h2>
          </div>
          <p className="text-sm text-muted-foreground">{t('ops.loadDesc')}</p>
          <div className="space-y-2">
            <label className="text-sm font-medium">{t('ops.pathOptional')}</label>
            <Input value={loadPath} onChange={(e) => setLoadPath(e.target.value)} placeholder={t('ops.autoDetect')} />
          </div>
          <Button onClick={() => loadMut.mutate()} disabled={loadMut.isPending} className="w-full">
            {loadMut.isPending ? t('ops.loading') : t('ops.load')}
          </Button>
          {loadMut.data && (
            <div className="rounded bg-green-50 p-3 text-sm dark:bg-green-950">
              <p>Loaded {loadMut.data.loadedKeys} keys, skipped {loadMut.data.skippedKeys}</p>
              <p className="text-muted-foreground">Duration: {loadMut.data.durationMs}ms</p>
            </div>
          )}
          {loadMut.error && <p className="text-sm text-destructive">{loadMut.error.message}</p>}
        </div>
      </div>

      <div className="rounded-lg border p-6">
        <div className="flex items-center gap-2 mb-4">
          <FileJson className="h-5 w-5" />
          <h2 className="text-lg font-semibold">{t('ops.cacheStats')}</h2>
        </div>
        {keysStats.data && (
          <div className="grid grid-cols-2 gap-4 sm:grid-cols-4">
            <div>
              <div className="text-xs text-muted-foreground">{t('ops.keyCount')}</div>
              <div className="text-xl font-bold">{keysStats.data.key_count}</div>
            </div>
            <div>
              <div className="text-xs text-muted-foreground">{t('ops.expirationHeap')}</div>
              <div className="text-xl font-bold">{keysStats.data.expiration_heap_size}</div>
            </div>
            <div>
              <div className="text-xs text-muted-foreground">{t('ops.evictionPolicy')}</div>
              <div className="text-xl font-bold">{keysStats.data.eviction_policy}</div>
            </div>
            <div>
              <div className="text-xs text-muted-foreground">{t('ops.approxMemory')}</div>
              <div className="text-xl font-bold">{keysStats.data.approximate_memory_bytes.toLocaleString()} B</div>
            </div>
          </div>
        )}
      </div>
    </div>
  );
}
