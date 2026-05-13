import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import {
  fetchKeys,
  fetchKeyValue,
  setKey,
  deleteKey,
  expireKey,
  resetCache,
  dumpCache,
  loadCache,
} from '../api/client';
import { useToast } from '../components/ui/toast';

export function useKeys(pattern: string, mode: 'WILDCARD' | 'REGEX' = 'WILDCARD') {
  return useQuery({
    queryKey: ['keys', pattern, mode],
    queryFn: () => fetchKeys(pattern, mode),
  });
}

export function useKeyValue(key: string) {
  return useQuery({
    queryKey: ['key', key],
    queryFn: () => fetchKeyValue(key),
    enabled: !!key,
  });
}

export function useSetKey() {
  const queryClient = useQueryClient();
  const { toast } = useToast();
  return useMutation({
    mutationFn: ({ key, value, expire }: { key: string; value: unknown; expire?: string }) =>
      setKey(key, value, expire),
    onSuccess: (_data, vars) => {
      queryClient.invalidateQueries({ queryKey: ['keys'] });
      toast('success', `Key "${vars.key}" set successfully`);
    },
    onError: (err: Error) => toast('error', err.message),
  });
}

export function useDeleteKey() {
  const queryClient = useQueryClient();
  const { toast } = useToast();
  return useMutation({
    mutationFn: (key: string) => deleteKey(key),
    onSuccess: (_data, key) => {
      queryClient.invalidateQueries({ queryKey: ['keys'] });
      toast('success', `Key "${key}" deleted`);
    },
    onError: (err: Error) => toast('error', err.message),
  });
}

export function useExpireKey() {
  const queryClient = useQueryClient();
  const { toast } = useToast();
  return useMutation({
    mutationFn: ({ key, expire }: { key: string; expire: string }) => expireKey(key, expire),
    onSuccess: (_data, vars) => {
      queryClient.invalidateQueries({ queryKey: ['keys'] });
      toast('success', `TTL set for "${vars.key}"`);
    },
    onError: (err: Error) => toast('error', err.message),
  });
}

export function useResetCache() {
  const queryClient = useQueryClient();
  const { toast } = useToast();
  return useMutation({
    mutationFn: resetCache,
    onSuccess: (data) => {
      queryClient.invalidateQueries({ queryKey: ['keys'] });
      toast('success', `Cache reset: ${data.keysCleared} keys cleared`);
    },
    onError: (err: Error) => toast('error', err.message),
  });
}

export function useDumpCache() {
  const { toast } = useToast();
  return useMutation({
    mutationFn: ({ format, path }: { format?: string; path?: string }) => dumpCache(format, path),
    onSuccess: (data) => toast('success', `Dumped ${data.totalKeys} keys to ${data.path}`),
    onError: (err: Error) => toast('error', err.message),
  });
}

export function useLoadCache() {
  const { toast } = useToast();
  return useMutation({
    mutationFn: (path?: string) => loadCache(path),
    onSuccess: (data) => toast('success', `Loaded ${data.loadedKeys} keys`),
    onError: (err: Error) => toast('error', err.message),
  });
}
