import { useQuery } from '@tanstack/react-query';
import { fetchStatus, fetchMetricsSummary } from '../api/client';

export function useStatus() {
  return useQuery({
    queryKey: ['status'],
    queryFn: fetchStatus,
    refetchInterval: 5000,
  });
}

export function useMetricsSummary() {
  return useQuery({
    queryKey: ['metrics'],
    queryFn: fetchMetricsSummary,
    refetchInterval: 5000,
  });
}
