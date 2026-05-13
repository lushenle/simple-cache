import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { fetchPeers, fetchRaftState, fetchHealth, joinNode, leaveNode, stepdownLeader } from '../api/client';
import { useToast } from '../components/ui/toast';

export function usePeers() {
  return useQuery({ queryKey: ['peers'], queryFn: fetchPeers, refetchInterval: 5000 });
}

export function useRaftState() {
  return useQuery({ queryKey: ['raft'], queryFn: fetchRaftState, refetchInterval: 5000 });
}

export function useHealth() {
  return useQuery({ queryKey: ['health'], queryFn: fetchHealth, refetchInterval: 5000 });
}

export function useJoinNode() {
  const queryClient = useQueryClient();
  const { toast } = useToast();
  return useMutation({
    mutationFn: ({ id, addr }: { id: string; addr: string }) => joinNode(id, addr),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['peers'] });
      toast('success', 'Node joined successfully');
    },
    onError: (err: Error) => toast('error', err.message),
  });
}

export function useLeaveNode() {
  const queryClient = useQueryClient();
  const { toast } = useToast();
  return useMutation({
    mutationFn: (addr: string) => leaveNode(addr),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['peers'] });
      toast('success', 'Node removed');
    },
    onError: (err: Error) => toast('error', err.message),
  });
}

export function useStepdown() {
  const queryClient = useQueryClient();
  const { toast } = useToast();
  return useMutation({
    mutationFn: stepdownLeader,
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['raft'] });
      toast('info', 'Leader stepped down');
    },
    onError: (err: Error) => toast('error', err.message),
  });
}
