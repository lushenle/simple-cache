import { useState } from 'react';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { fetchKeys, fetchKeyValue, setKey, deleteKey, expireKey, resetCache } from '../api/client';
import { useI18n } from '../i18n/I18nProvider';
import { Button } from '../components/ui/button';
import { Input } from '../components/ui/input';
import { Select } from '../components/ui/select';
import { Dialog } from '../components/ui/dialog';
import { Table, TableHeader, TableBody, TableRow, TableHead, TableCell } from '../components/ui/table';
import { Search, Plus, Trash2, Clock, Eye, RefreshCw } from 'lucide-react';

export default function CacheBrowser() {
  const queryClient = useQueryClient();
  const [pattern, setPattern] = useState('');
  const [searchInput, setSearchInput] = useState('');
  const [mode, setMode] = useState('WILDCARD');
  const [selectedKey, setSelectedKey] = useState<string | null>(null);
  const [newKeyOpen, setNewKeyOpen] = useState(false);
  const [expireOpen, setExpireOpen] = useState(false);
  const [newKeyName, setNewKeyName] = useState('');
  const [newKeyValue, setNewKeyValue] = useState('');
  const [newKeyTTL, setNewKeyTTL] = useState('');
  const [expireTTL, setExpireTTL] = useState('');
  const [valueView, setValueView] = useState<{ key: string; value: unknown } | null>(null);
  const { t } = useI18n();

  const keys = useQuery({
    queryKey: ['keys', pattern, mode],
    queryFn: () => fetchKeys(pattern, mode as 'WILDCARD' | 'REGEX'),
    enabled: true,
  });

  const setMut = useMutation({
    mutationFn: () => setKey(newKeyName, newKeyValue, newKeyTTL || undefined),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['keys'] });
      setNewKeyOpen(false);
      setNewKeyName('');
      setNewKeyValue('');
      setNewKeyTTL('');
    },
  });

  const delMut = useMutation({
    mutationFn: (key: string) => deleteKey(key),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['keys'] }),
  });

  const expireMut = useMutation({
    mutationFn: () => expireKey(selectedKey!, expireTTL),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['keys'] });
      setExpireOpen(false);
      setSelectedKey(null);
      setExpireTTL('');
    },
  });

  const resetMut = useMutation({
    mutationFn: resetCache,
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['keys'] }),
  });

  const viewKey = async (key: string) => {
    try {
      const res = await fetchKeyValue(key);
      setValueView({ key, value: res.value });
    } catch { /* ignore */ }
  };

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <h1 className="text-2xl font-bold">{t('cache.title')}</h1>
        <div className="flex gap-2">
          <Button onClick={() => setNewKeyOpen(true)} size="sm">
            <Plus className="h-4 w-4" /> {t('cache.newKey')}
          </Button>
          <Button onClick={() => keys.refetch()} variant="outline" size="sm">
            <RefreshCw className="h-4 w-4" /> {t('cache.refresh')}
          </Button>
          <Button onClick={() => { if (confirm(t('cache.confirmReset'))) resetMut.mutate(); }} variant="destructive" size="sm">
            <Trash2 className="h-4 w-4" /> {t('cache.reset')}
          </Button>
        </div>
      </div>

      <div className="flex gap-2">
        <Input
          placeholder={t('cache.searchPlaceholder')}
          value={searchInput}
          onChange={(e) => setSearchInput(e.target.value)}
          onKeyDown={(e) => { if (e.key === 'Enter') setPattern(searchInput); }}
          className="max-w-sm"
        />
        <Select value={mode} onChange={setMode} options={[
          { value: 'WILDCARD', label: 'Wildcard' },
          { value: 'REGEX', label: 'Regex' },
        ]} className="w-28" />
        <Button onClick={() => setPattern(searchInput)}>
          <Search className="h-4 w-4" /> {t('cache.search')}
        </Button>
      </div>

      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>{t('cache.key')}</TableHead>
            <TableHead className="w-40">{t('cache.actions')}</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {keys.data?.keys?.map((key) => (
            <TableRow key={key}>
              <TableCell className="font-mono text-sm">{key}</TableCell>
              <TableCell>
                <div className="flex gap-1">
                  <Button variant="ghost" size="sm" onClick={() => viewKey(key)}>
                    <Eye className="h-3 w-3" />
                  </Button>
                  <Button variant="ghost" size="sm" onClick={() => { setSelectedKey(key); setExpireOpen(true); }}>
                    <Clock className="h-3 w-3" />
                  </Button>
                  <Button variant="ghost" size="sm" onClick={() => { if (confirm(`${t('cache.confirmDelete')} ${key}?`)) delMut.mutate(key); }}>
                    <Trash2 className="h-3 w-3 text-destructive" />
                  </Button>
                </div>
              </TableCell>
            </TableRow>
          ))}
          {keys.data?.keys?.length === 0 && (
            <TableRow>
              <TableCell colSpan={2} className="text-center text-muted-foreground py-8">
                {t('cache.noKeys')}
              </TableCell>
            </TableRow>
          )}
        </TableBody>
      </Table>

      {valueView && (
        <Dialog open={!!valueView} onClose={() => setValueView(null)} title={`${t('cache.key')}: ${valueView.key}`}>
          <div className="max-h-80 overflow-auto">
            <pre className="whitespace-pre-wrap break-all text-sm">
              {typeof valueView.value === 'object'
                ? JSON.stringify(valueView.value, null, 2)
                : String(valueView.value)}
            </pre>
          </div>
        </Dialog>
      )}

      <Dialog open={newKeyOpen} onClose={() => setNewKeyOpen(false)} title={t('cache.newKey')}>
        <div className="space-y-4">
          <div>
            <label className="text-sm font-medium">{t('cache.key')}</label>
            <Input value={newKeyName} onChange={(e) => setNewKeyName(e.target.value)} placeholder={t('cache.newKeyPlaceholder')} />
          </div>
          <div>
            <label className="text-sm font-medium">{t('cache.value')}</label>
            <Input value={newKeyValue} onChange={(e) => setNewKeyValue(e.target.value)} placeholder={t('cache.valuePlaceholder')} />
          </div>
          <div>
            <label className="text-sm font-medium">{t('cache.ttlOptional')}</label>
            <Input value={newKeyTTL} onChange={(e) => setNewKeyTTL(e.target.value)} placeholder={t('cache.ttlPlaceholder')} />
          </div>
          {setMut.error && <p className="text-sm text-destructive">{setMut.error.message}</p>}
          <Button onClick={() => setMut.mutate()} disabled={!newKeyName || setMut.isPending} className="w-full">
            {setMut.isPending ? t('cache.setting') : t('cache.setKey')}
          </Button>
        </div>
      </Dialog>

      <Dialog open={expireOpen} onClose={() => setExpireOpen(false)} title={`${t('cache.setTtl')}: ${selectedKey}`}>
        <div className="space-y-4">
          <div>
            <label className="text-sm font-medium">{t('cache.newTtl')}</label>
            <Input value={expireTTL} onChange={(e) => setExpireTTL(e.target.value)} placeholder={t('cache.ttlPlaceholder')} />
          </div>
          <Button onClick={() => expireMut.mutate()} disabled={!expireTTL || expireMut.isPending} className="w-full">
            {expireMut.isPending ? t('cache.setting') : t('cache.setTtl')}
          </Button>
        </div>
      </Dialog>
    </div>
  );
}
