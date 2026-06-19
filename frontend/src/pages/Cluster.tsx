import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { useState } from 'react';
import { fetchClusterNodes, fetchRaftState, joinNode, leaveNode, stepdownLeader } from '../api/client';
import { useI18n } from '../i18n/I18nProvider';
import { Button } from '../components/ui/button';
import { Input } from '../components/ui/input';
import { Dialog } from '../components/ui/dialog';
import { Badge } from '../components/ui/badge';
import { Table, TableHeader, TableBody, TableRow, TableHead, TableCell } from '../components/ui/table';
import { Plus, UserMinus, ArrowDown } from 'lucide-react';

export default function Cluster() {
  const queryClient = useQueryClient();
  const [joinOpen, setJoinOpen] = useState(false);
  const [leaveOpen, setLeaveOpen] = useState(false);
  const [joinId, setJoinId] = useState('');
  const [joinAddr, setJoinAddr] = useState('');
  const [leaveAddr, setLeaveAddr] = useState('');
  const { t } = useI18n();

  const nodes = useQuery({ queryKey: ['clusterNodes'], queryFn: fetchClusterNodes, refetchInterval: 5000 });
  const raft = useQuery({ queryKey: ['raft'], queryFn: fetchRaftState, refetchInterval: 5000 });

  const joinMut = useMutation({
    mutationFn: () => joinNode(joinId, joinAddr),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['clusterNodes'] });
      setJoinOpen(false);
      setJoinId('');
      setJoinAddr('');
    },
  });

  const leaveMut = useMutation({
    mutationFn: () => leaveNode(leaveAddr),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['clusterNodes'] });
      setLeaveOpen(false);
      setLeaveAddr('');
    },
  });

  const stepdownMut = useMutation({
    mutationFn: stepdownLeader,
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['raft', 'clusterNodes'] }),
  });

  const roleBadge = (role: string) => {
    const colors: Record<string, string> = {
      leader: 'bg-green-100 text-green-800 dark:bg-green-900 dark:text-green-100',
      follower: 'bg-blue-100 text-blue-800 dark:bg-blue-900 dark:text-blue-100',
      candidate: 'bg-yellow-100 text-yellow-800 dark:bg-yellow-900 dark:text-yellow-100',
    };
    return colors[role] || 'bg-gray-100 text-gray-800';
  };

  const roleLabel = (role: string) => {
    if (role === 'leader') return t('cluster.leaderRole');
    if (role === 'follower') return t('cluster.followerRole');
    return role;
  };

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <h1 className="text-2xl font-bold">{t('cluster.title')}</h1>
        <div className="flex gap-2">
          <Button onClick={() => setJoinOpen(true)} size="sm">
            <Plus className="h-4 w-4" /> {t('cluster.joinNode')}
          </Button>
          <Button onClick={() => setLeaveOpen(true)} variant="outline" size="sm">
            <UserMinus className="h-4 w-4" /> {t('cluster.leaveNode')}
          </Button>
          <Button onClick={() => stepdownMut.mutate()} variant="outline" size="sm">
            <ArrowDown className="h-4 w-4" /> {t('cluster.stepdown')}
          </Button>
        </div>
      </div>

      {raft.data && (
        <div className="grid grid-cols-2 gap-4 sm:grid-cols-4">
          <div className="rounded-lg border p-3">
            <div className="text-xs text-muted-foreground">{t('cluster.term')}</div>
            <div className="text-lg font-bold">{raft.data.term ?? '-'}</div>
          </div>
          <div className="rounded-lg border p-3">
            <div className="text-xs text-muted-foreground">{t('cluster.commitIndex')}</div>
            <div className="text-lg font-bold">{raft.data.commit_index ?? '-'}</div>
          </div>
          <div className="rounded-lg border p-3">
            <div className="text-xs text-muted-foreground">{t('cluster.lastApplied')}</div>
            <div className="text-lg font-bold">{raft.data.last_applied ?? '-'}</div>
          </div>
          <div className="rounded-lg border p-3">
            <div className="text-xs text-muted-foreground">{t('cluster.leader')}</div>
            <div className="text-lg font-bold">{nodes.data?.leader_id ?? '-'}</div>
          </div>
        </div>
      )}

      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>{t('cluster.nodeId')}</TableHead>
            <TableHead>{t('cluster.address')}</TableHead>
            <TableHead>{t('cluster.role')}</TableHead>
            <TableHead>{t('cluster.status')}</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {nodes.data?.nodes?.map((node) => (
            <TableRow key={node.node_id}>
              <TableCell className="font-medium">{node.node_id}</TableCell>
              <TableCell className="font-mono text-sm">{node.address}</TableCell>
              <TableCell>
                <Badge className={roleBadge(node.role)}>{roleLabel(node.role)}</Badge>
              </TableCell>
              <TableCell>
                <Badge className={node.status === 'ok' ? 'bg-green-100 text-green-800' : 'bg-red-100 text-red-800'}>
                  {node.status === 'ok' ? t('cluster.ok') : node.status}
                </Badge>
              </TableCell>
            </TableRow>
          ))}
          {(!nodes.data?.nodes || nodes.data.nodes.length === 0) && (
            <TableRow>
              <TableCell colSpan={4} className="text-center text-muted-foreground py-8">
                {t('cluster.noPeers')}
              </TableCell>
            </TableRow>
          )}
        </TableBody>
      </Table>

      <Dialog open={joinOpen} onClose={() => setJoinOpen(false)} title={t('cluster.joinNode')}>
        <div className="space-y-4">
          <div>
            <label className="text-sm font-medium">{t('cluster.nodeId')}</label>
            <Input value={joinId} onChange={(e) => setJoinId(e.target.value)} placeholder={t('cluster.nodeIdPlaceholder')} />
          </div>
          <div>
            <label className="text-sm font-medium">{t('cluster.address')}</label>
            <Input value={joinAddr} onChange={(e) => setJoinAddr(e.target.value)} placeholder={t('cluster.raftAddrPlaceholder')} />
          </div>
          {joinMut.error && <p className="text-sm text-destructive">{joinMut.error.message}</p>}
          <Button onClick={() => joinMut.mutate()} disabled={!joinAddr || joinMut.isPending} className="w-full">
            {joinMut.isPending ? t('cluster.joining') : t('cluster.join')}
          </Button>
        </div>
      </Dialog>

      <Dialog open={leaveOpen} onClose={() => setLeaveOpen(false)} title={t('cluster.leaveNode')}>
        <div className="space-y-4">
          <div>
            <label className="text-sm font-medium">{t('cluster.address')}</label>
            <Input value={leaveAddr} onChange={(e) => setLeaveAddr(e.target.value)} placeholder={t('cluster.leaveAddrPlaceholder')} />
          </div>
          {leaveMut.error && <p className="text-sm text-destructive">{leaveMut.error.message}</p>}
          <Button onClick={() => leaveMut.mutate()} disabled={!leaveAddr || leaveMut.isPending} variant="destructive" className="w-full">
            {leaveMut.isPending ? t('cluster.removing') : t('cluster.remove')}
          </Button>
        </div>
      </Dialog>
    </div>
  );
}
