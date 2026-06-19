import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { fetchSubscriptions, killSubscription } from '../api/client';
import { useToast } from '../components/ui/toast';

export function useSubscriptionsList() {
  return useQuery({
    queryKey: ['subscriptions'],
    queryFn: fetchSubscriptions,
    refetchInterval: 5000,
  });
}

export function useKillSubscription() {
  const queryClient = useQueryClient();
  const { toast } = useToast();
  return useMutation({
    mutationFn: (id: string) => killSubscription(id),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['subscriptions'] });
      toast('success', 'Subscription terminated');
    },
    onError: (err: Error) => toast('error', err.message),
  });
}
