const API_BASE = '/admin/api';
const CACHE_BASE = '/v1';
const CLUSTER_BASE = '/cluster';

function getToken(): string | null {
  return localStorage.getItem('admin-token');
}

export function setToken(token: string) {
  localStorage.setItem('admin-token', token);
}

export function clearToken() {
  localStorage.removeItem('admin-token');
}

export function hasToken(): boolean {
  return getToken() !== null;
}

class ApiError extends Error {
  status: number;
  constructor(message: string, status: number) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
  }
}

async function request<T>(url: string, options: RequestInit = {}): Promise<T> {
  const token = getToken();
  const headers: Record<string, string> = {
    ...(options.headers as Record<string, string>),
  };
  if (token) {
    headers['x-api-token'] = token;
  }

  const res = await fetch(url, {
    ...options,
    headers,
  });

  if (res.status === 401) {
    clearToken();
    window.dispatchEvent(new CustomEvent('auth:expired'));
    throw new ApiError('Unauthorized', 401);
  }

  if (!res.ok) {
    const text = await res.text();
    throw new ApiError(text || res.statusText, res.status);
  }

  return res.json();
}

// ---- Admin API ----

export interface AdminStatus {
  node_id: string;
  mode: string;
  role: string;
  ready: boolean;
  leader_id?: string;
  grpc_addr?: string;
  uptime: string;
  keys_total: number;
  memory_alloc: number;
  memory_total: number;
  cache_stats: {
    key_count: number;
    expiration_heap: number;
    eviction_policy: string;
    max_keys: number;
    max_value_size: number;
    approximate_memory: number;
  };
}

export function fetchStatus(): Promise<AdminStatus> {
  return request<AdminStatus>(`${API_BASE}/status`);
}

export interface MetricsSummary {
  keys_total: number;
  expiration_heap_size: number;
  memory_alloc: number;
  memory_total: number;
  evictions_total: number;
  raft_role: string;
  raft_commit_index: number;
  raft_last_applied: number;
  peers_total: number;
  raft_pending_entries: number;
  raft_snapshot_age: number;
  requests_by_op: Record<string, { success: number; error: number }>;
  persistence_ops: Record<string, { success: number; error: number }>;
}

export function fetchMetricsSummary(): Promise<MetricsSummary> {
  return request<MetricsSummary>(`${API_BASE}/metrics/summary`);
}

export interface AdminConfig {
  mode: string;
  node_id: string;
  grpc_addr: string;
  http_addr: string;
  raft_http_addr: string;
  metrics_addr: string;
  peers: string[];
  peer_addresses?: Record<string, string>;
  heartbeat_ms: number;
  election_ms: number;
  hot_reload: boolean;
  load_on_startup: boolean;
  dump_on_shutdown: boolean;
  dump_format: string;
  data_dir: string;
  auth_enabled: boolean;
  tls_enabled: boolean;
  allowed_origins: string[];
  snapshot_enabled: boolean;
  snapshot_threshold: number;
  max_keys: number;
  max_value_size: number;
  max_qps: number;
  eviction_policy: string;
}

export function fetchConfig(): Promise<AdminConfig> {
  return request<AdminConfig>(`${API_BASE}/config`);
}

export interface SubscriptionInfo {
  id: string;
  pattern: string;
}

export function fetchSubscriptions(): Promise<SubscriptionInfo[]> {
  return request<SubscriptionInfo[]>(`${API_BASE}/subscriptions`);
}

export function killSubscription(id: string): Promise<{ status: string }> {
  return request(`${API_BASE}/subscriptions/${id}`, { method: 'DELETE' });
}

export interface RaftState {
  mode?: string;
  node_id?: string;
  role?: string;
  leader_id?: string;
  term?: number;
  commit_index?: number;
  last_applied?: number;
  peers?: string[];
  [key: string]: unknown;
}

export function fetchRaftState(): Promise<RaftState> {
  return request<RaftState>(`${API_BASE}/raft`);
}

export interface KeysStats {
  key_count: number;
  expiration_heap_size: number;
  eviction_policy: string;
  max_keys: number;
  max_value_size: number;
  approximate_memory_bytes: number;
}

export function fetchKeysStats(): Promise<KeysStats> {
  return request<KeysStats>(`${API_BASE}/keys/stats`);
}

// ---- Cache API ----

export function fetchKeys(pattern: string, mode: 'WILDCARD' | 'REGEX' = 'WILDCARD'): Promise<{ keys: string[] }> {
  const params = new URLSearchParams({ pattern, mode });
  return request(`${CACHE_BASE}/search?${params}`);
}

export function fetchKeyValue(key: string): Promise<{ value: unknown; found: boolean }> {
  return request(`${CACHE_BASE}/${encodeURIComponent(key)}`);
}

export function setKey(key: string, value: unknown, expire?: string): Promise<{ success: boolean }> {
  return request(`${API_BASE}/set/${encodeURIComponent(key)}`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ value, expire: expire || '' }),
  });
}

export function deleteKey(key: string): Promise<{ success: boolean; existed: boolean }> {
  return request(`${CACHE_BASE}/${encodeURIComponent(key)}`, { method: 'DELETE' });
}

export function expireKey(key: string, expire: string): Promise<{ success: boolean; existed: boolean }> {
  return request(`${CACHE_BASE}/${encodeURIComponent(key)}/expire?expire=${expire}`, { method: 'POST' });
}

export function resetCache(): Promise<{ success: boolean; keysCleared: number }> {
  return request(`${CACHE_BASE}`, { method: 'DELETE' });
}

export function dumpCache(format: string = 'binary', path?: string): Promise<{
  success: boolean;
  totalKeys: number;
  fileSize: string;
  path: string;
  format: string;
  durationMs: number;
}> {
  return request(`${CACHE_BASE}/dump`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ format, path: path || '' }),
  });
}

export function loadCache(path?: string): Promise<{
  success: boolean;
  totalKeys: number;
  loadedKeys: number;
  skippedKeys: number;
  path: string;
  durationMs: number;
}> {
  return request(`${CACHE_BASE}/load`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ path: path || '' }),
  });
}

// ---- Cluster API ----

export function fetchPeers(): Promise<string[]> {
  return request(`${CLUSTER_BASE}/peers`);
}

export interface ClusterNodeInfo {
  address: string;
  node_id: string;
  role: string;
  is_leader: boolean;
  status: string;
}

export interface ClusterNodesResponse {
  mode: string;
  nodes: ClusterNodeInfo[];
  leader_id: string;
  peers_total: number;
}

export function fetchClusterNodes(): Promise<ClusterNodesResponse> {
  return request(`${API_BASE}/cluster/nodes`);
}

export function joinNode(id: string, addr: string): Promise<string> {
  return request(`${CLUSTER_BASE}/join`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ id, addr }),
  });
}

export function leaveNode(addr: string): Promise<string> {
  return request(`${CLUSTER_BASE}/leave`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ addr }),
  });
}

export function stepdownLeader(): Promise<string> {
  return request(`${CLUSTER_BASE}/stepdown`, { method: 'POST' });
}

export function fetchHealth(): Promise<{
  status: string;
  mode: string;
  ready: boolean;
  role: string;
  leader_id?: string;
  leader_grpc_addr?: string;
  grpc_addr?: string;
  details?: Record<string, unknown>;
}> {
  return request('/healthz');
}
