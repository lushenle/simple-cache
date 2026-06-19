import { useState, useCallback, useEffect, useRef } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { fetchSubscriptions, killSubscription, hasToken } from '../api/client';
import { useI18n } from '../i18n/I18nProvider';
import { Button } from '../components/ui/button';
import { Input } from '../components/ui/input';
import { Badge } from '../components/ui/badge';
import { Table, TableHeader, TableBody, TableRow, TableHead, TableCell } from '../components/ui/table';
import { Plus, X, Pause, Play, Trash2 } from 'lucide-react';

interface StreamEvent {
  type: string;
  key: string;
  value?: unknown;
}

export default function Subscriptions() {
  const queryClient = useQueryClient();
  const [pattern, setPattern] = useState('');
  const [events, setEvents] = useState<StreamEvent[]>([]);
  const [paused, setPaused] = useState(false);
  const [connected, setConnected] = useState(false);
  const eventSourceRef = useRef<EventSource | null>(null);
  const { t } = useI18n();

  const subs = useQuery({
    queryKey: ['subscriptions'],
    queryFn: fetchSubscriptions,
    refetchInterval: 5000,
  });

  const subscribe = useCallback(() => {
    if (eventSourceRef.current) eventSourceRef.current.close();
    const token = hasToken() ? localStorage.getItem('admin-token') : '';
    const url = `/admin/api/watch?pattern=${encodeURIComponent(pattern)}&token=${encodeURIComponent(token || '')}`;
    const es = new EventSource(url);
    eventSourceRef.current = es;

    es.addEventListener('connected', () => setConnected(true));
    es.addEventListener('message', (e) => {
      if (!paused) {
        try {
          const evt = JSON.parse(e.data);
          setEvents((prev) => [evt, ...prev].slice(0, 200));
        } catch { /* ignore */ }
      }
    });
    es.onerror = () => setConnected(false);
  }, [pattern, paused]);

  useEffect(() => {
    return () => { if (eventSourceRef.current) eventSourceRef.current.close(); };
  }, []);

  const eventBadge = (type: string) => {
    const colors: Record<string, string> = {
      EVENT_SET: 'bg-green-100 text-green-800',
      EVENT_DEL: 'bg-red-100 text-red-800',
      EVENT_EXPIRE: 'bg-yellow-100 text-yellow-800',
    };
    return colors[type] || 'bg-gray-100 text-gray-800';
  };

  return (
    <div className="space-y-6">
      <h1 className="text-2xl font-bold">{t('sub.title')}</h1>

      <div className="flex items-center gap-2">
        <Input
          placeholder={t('sub.pattern')}
          value={pattern}
          onChange={(e) => setPattern(e.target.value)}
          className="max-w-sm"
        />
        <Button onClick={subscribe}>
          <Plus className="h-4 w-4" /> {t('sub.subscribe')}
        </Button>
        {connected && <Badge className="bg-green-100 text-green-800">{t('sub.connected')}</Badge>}
        {!connected && eventSourceRef.current && <Badge className="bg-red-100 text-red-800">{t('sub.disconnected')}</Badge>}
      </div>

      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>{t('sub.id')}</TableHead>
            <TableHead>{t('sub.pattern')}</TableHead>
            <TableHead className="w-20">{t('cache.actions')}</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {subs.data?.map((sub) => (
            <TableRow key={sub.id}>
              <TableCell className="font-mono text-sm">{sub.id}</TableCell>
              <TableCell>{sub.pattern || t('sub.all')}</TableCell>
              <TableCell>
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={() => killSubscription(sub.id).then(() =>
                    queryClient.invalidateQueries({ queryKey: ['subscriptions'] }),
                  )}
                >
                  <X className="h-3 w-3 text-destructive" />
                </Button>
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>

      <div className="space-y-2">
        <div className="flex items-center justify-between">
          <h2 className="text-lg font-semibold">{t('sub.eventStream')}</h2>
          <div className="flex gap-2">
            <Button variant="outline" size="sm" onClick={() => setPaused(!paused)}>
              {paused ? <Play className="h-3 w-3" /> : <Pause className="h-3 w-3" />}
              {paused ? t('sub.resume') : t('sub.pause')}
            </Button>
            <Button variant="outline" size="sm" onClick={() => setEvents([])}>
              <Trash2 className="h-3 w-3" /> {t('sub.clear')}
            </Button>
          </div>
        </div>
        <div className="max-h-96 overflow-auto rounded-lg border">
          {events.length === 0 ? (
            <div className="py-12 text-center text-muted-foreground">{t('sub.noEvents')}</div>
          ) : (
            events.map((evt, i) => (
              <div key={i} className="flex items-center gap-2 border-b px-4 py-2 font-mono text-sm">
                <Badge className={eventBadge(evt.type)}>
                  {evt.type?.replace('EVENT_', '')}
                </Badge>
                <span>{evt.key}</span>
                {evt.value !== undefined && (
                  <span className="text-muted-foreground">
                    = {typeof evt.value === 'object' ? JSON.stringify(evt.value) : String(evt.value)}
                  </span>
                )}
              </div>
            ))
          )}
        </div>
      </div>
    </div>
  );
}
