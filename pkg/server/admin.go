package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"runtime"
	"strings"
	"time"

	"github.com/lushenle/simple-cache/pkg/config"
	"github.com/lushenle/simple-cache/pkg/pb"
	"github.com/lushenle/simple-cache/pkg/utils"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// AdminHandler serves the admin API endpoints used by the frontend SPA.
type AdminHandler struct {
	srv *CacheService
	cfg *config.AtomicConfig
}

// NewAdminHandler creates an AdminHandler.
func NewAdminHandler(srv *CacheService, cfg *config.AtomicConfig) *AdminHandler {
	return &AdminHandler{srv: srv, cfg: cfg}
}

// Register mounts admin API routes on the given mux.
func (h *AdminHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("/admin/api/status", h.status)
	mux.HandleFunc("/admin/api/metrics/summary", h.metricsSummary)
	mux.HandleFunc("/admin/api/config", h.configView)
	mux.HandleFunc("/admin/api/subscriptions", h.subscriptions)
	mux.HandleFunc("/admin/api/subscriptions/", h.subscriptionByID)
	mux.HandleFunc("/admin/api/cluster/nodes", h.clusterNodes)
	mux.HandleFunc("/admin/api/raft", h.raftState)
	mux.HandleFunc("/admin/api/keys/stats", h.keysStats)
	mux.HandleFunc("/admin/api/set/", h.adminSet)
	mux.HandleFunc("/admin/api/watch", h.watchSSE)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// ---------- GET /admin/api/status ----------

type adminStatusResponse struct {
	NodeID      string         `json:"node_id"`
	Mode        string         `json:"mode"`
	Role        string         `json:"role"`
	Ready       bool           `json:"ready"`
	LeaderID    string         `json:"leader_id,omitempty"`
	GRPCAddr    string         `json:"grpc_addr,omitempty"`
	Uptime      string         `json:"uptime"`
	KeysTotal   int            `json:"keys_total"`
	MemoryAlloc uint64         `json:"memory_alloc"`
	MemoryTotal uint64         `json:"memory_total"`
	CacheStats  map[string]any `json:"cache_stats,omitempty"`
}

var startTime = time.Now()

func (h *AdminHandler) status(w http.ResponseWriter, r *http.Request) {
	hs := h.srv.HealthStatus()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	cacheStats := h.srv.fsm.Cache.Stats()

	resp := adminStatusResponse{
		NodeID:      h.srv.nodeID,
		Mode:        string(hs.Mode),
		Role:        hs.Role,
		Ready:       hs.Ready,
		LeaderID:    hs.LeaderID,
		GRPCAddr:    hs.GRPCAddr,
		Uptime:      time.Since(startTime).Truncate(time.Second).String(),
		KeysTotal:   cacheStats.KeyCount,
		MemoryAlloc: m.Alloc,
		MemoryTotal: m.Sys,
		CacheStats: map[string]any{
			"key_count":          cacheStats.KeyCount,
			"expiration_heap":    cacheStats.ExpirationHeapSize,
			"eviction_policy":    cacheStats.EvictionPolicy,
			"max_keys":           cacheStats.MaxKeys,
			"max_value_size":     cacheStats.MaxValueSize,
			"approximate_memory": cacheStats.ApproximateMemoryBytes,
		},
	}
	writeJSON(w, http.StatusOK, resp)
}

// ---------- GET /admin/api/metrics/summary ----------

type metricsSummaryResponse struct {
	KeysTotal       float64            `json:"keys_total"`
	ExpirationHeap  float64            `json:"expiration_heap_size"`
	MemoryAlloc     float64            `json:"memory_alloc"`
	MemoryTotal     float64            `json:"memory_total"`
	EvictionsTotal  float64            `json:"evictions_total"`
	RaftRole        string             `json:"raft_role"`
	RaftCommitIndex float64            `json:"raft_commit_index"`
	RaftLastApplied float64            `json:"raft_last_applied"`
	PeersTotal      float64            `json:"peers_total"`
	PendingEntries  float64            `json:"raft_pending_entries"`
	SnapshotAge     float64            `json:"raft_snapshot_age"`
	RequestsByOp    map[string]opCount `json:"requests_by_op"`
	PersistenceOps  map[string]opCount `json:"persistence_ops"`
}

type opCount struct {
	Success float64 `json:"success"`
	Error   float64 `json:"error"`
}

func (h *AdminHandler) metricsSummary(w http.ResponseWriter, r *http.Request) {
	gathered, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to gather metrics")
		return
	}

	resp := metricsSummaryResponse{
		RequestsByOp:   make(map[string]opCount),
		PersistenceOps: make(map[string]opCount),
	}

	for _, fam := range gathered {
		switch fam.GetName() {
		case "simple_cache_keys_total":
			resp.KeysTotal = getGaugeValue(fam)
		case "simple_cache_expiration_heap_size":
			resp.ExpirationHeap = getGaugeValue(fam)
		case "process_memory_bytes":
			for _, m := range fam.GetMetric() {
				for _, l := range m.GetLabel() {
					if l.GetName() == "state" {
						switch l.GetValue() {
						case "alloc":
							resp.MemoryAlloc = m.GetGauge().GetValue()
						case "total":
							resp.MemoryTotal = m.GetGauge().GetValue()
						}
					}
				}
			}
		case "cache_evictions_total":
			resp.EvictionsTotal = getCounterValue(fam)
		case "raft_role":
			for _, m := range fam.GetMetric() {
				if m.GetGauge().GetValue() == 1 {
					for _, l := range m.GetLabel() {
						if l.GetName() == "role" {
							resp.RaftRole = l.GetValue()
						}
					}
				}
			}
		case "raft_commit_index":
			resp.RaftCommitIndex = getGaugeValue(fam)
		case "raft_last_applied":
			resp.RaftLastApplied = getGaugeValue(fam)
		case "simple_cache_peers_total":
			resp.PeersTotal = getGaugeValue(fam)
		case "raft_pending_entries":
			resp.PendingEntries = getGaugeValue(fam)
		case "raft_snapshot_age_seconds":
			resp.SnapshotAge = getGaugeValue(fam)
		case "simple_cache_requests_total":
			for _, m := range fam.GetMetric() {
				op := ""
				status := ""
				for _, l := range m.GetLabel() {
					switch l.GetName() {
					case "op":
						op = l.GetValue()
					case "status":
						status = l.GetValue()
					}
				}
				oc := resp.RequestsByOp[op]
				switch status {
				case "success":
					oc.Success = m.GetCounter().GetValue()
				case "error":
					oc.Error = m.GetCounter().GetValue()
				}
				resp.RequestsByOp[op] = oc
			}
		case "cache_persistence_op_total":
			for _, m := range fam.GetMetric() {
				op := ""
				status := ""
				for _, l := range m.GetLabel() {
					switch l.GetName() {
					case "op":
						op = l.GetValue()
					case "status":
						status = l.GetValue()
					}
				}
				oc := resp.PersistenceOps[op]
				switch status {
				case "success":
					oc.Success = m.GetCounter().GetValue()
				case "error":
					oc.Error = m.GetCounter().GetValue()
				}
				resp.PersistenceOps[op] = oc
			}
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

func getGaugeValue(fam *dto.MetricFamily) float64 {
	if m := fam.GetMetric(); len(m) > 0 {
		return m[0].GetGauge().GetValue()
	}
	return 0
}

func getCounterValue(fam *dto.MetricFamily) float64 {
	if m := fam.GetMetric(); len(m) > 0 {
		return m[0].GetCounter().GetValue()
	}
	return 0
}

// ---------- GET /admin/api/config ----------

func (h *AdminHandler) configView(w http.ResponseWriter, r *http.Request) {
	cfg := h.cfg.Get()
	type configView struct {
		Mode              string            `json:"mode"`
		NodeID            string            `json:"node_id"`
		GRPCAddr          string            `json:"grpc_addr"`
		HTTPAddr          string            `json:"http_addr"`
		RaftHTTPAddr      string            `json:"raft_http_addr"`
		MetricsAddr       string            `json:"metrics_addr"`
		Peers             []string          `json:"peers"`
		PeerAddresses     map[string]string `json:"peer_addresses,omitempty"`
		HeartbeatMS       int               `json:"heartbeat_ms"`
		ElectionMS        int               `json:"election_ms"`
		HotReload         bool              `json:"hot_reload"`
		LoadOnStartup     bool              `json:"load_on_startup"`
		DumpOnShutdown    bool              `json:"dump_on_shutdown"`
		DumpFormat        string            `json:"dump_format"`
		DataDir           string            `json:"data_dir"`
		AuthEnabled       bool              `json:"auth_enabled"`
		TLSEnabled        bool              `json:"tls_enabled"`
		AllowedOrigins    []string          `json:"allowed_origins"`
		SnapshotEnabled   bool              `json:"snapshot_enabled"`
		SnapshotThreshold uint64            `json:"snapshot_threshold"`
		MaxKeys           int               `json:"max_keys"`
		MaxValueSize      int               `json:"max_value_size"`
		MaxQPS            int               `json:"max_qps"`
		EvictionPolicy    string            `json:"eviction_policy"`
	}
	v := configView{
		Mode:              string(cfg.Mode),
		NodeID:            cfg.NodeID,
		GRPCAddr:          cfg.GRPCAddr,
		HTTPAddr:          cfg.HTTPAddr,
		RaftHTTPAddr:      cfg.RaftHTTPAddr,
		MetricsAddr:       cfg.MetricsAddr,
		Peers:             cfg.Peers,
		PeerAddresses:     cfg.PeerAddresses,
		HeartbeatMS:       cfg.HeartbeatMS,
		ElectionMS:        cfg.ElectionMS,
		HotReload:         cfg.HotReload,
		LoadOnStartup:     cfg.LoadOnStartup,
		DumpOnShutdown:    cfg.DumpOnShutdown,
		DumpFormat:        cfg.DumpFormat.String(),
		DataDir:           cfg.DataDir,
		AuthEnabled:       cfg.AuthToken != "",
		TLSEnabled:        cfg.EnableTLS,
		AllowedOrigins:    cfg.AllowedOrigins,
		SnapshotEnabled:   cfg.SnapshotEnabled,
		SnapshotThreshold: cfg.SnapshotThreshold,
		MaxKeys:           cfg.MaxKeys,
		MaxValueSize:      cfg.MaxValueSize,
		MaxQPS:            cfg.MaxQPS,
		EvictionPolicy:    cfg.EvictionPolicy,
	}
	writeJSON(w, http.StatusOK, v)
}

// ---------- GET /admin/api/subscriptions ----------

type subscriptionInfo struct {
	ID      string `json:"id"`
	Pattern string `json:"pattern"`
}

func (h *AdminHandler) subscriptions(w http.ResponseWriter, r *http.Request) {
	if h.srv.watchSvc == nil {
		writeJSON(w, http.StatusOK, []subscriptionInfo{})
		return
	}
	subs := h.srv.watchSvc.List()
	out := make([]subscriptionInfo, 0, len(subs))
	for _, s := range subs {
		out = append(out, subscriptionInfo{
			ID:      s.ID,
			Pattern: s.Pattern,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// ---------- DELETE /admin/api/subscriptions/:id ----------

func (h *AdminHandler) subscriptionByID(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/admin/api/subscriptions/")
	if id == "" {
		writeError(w, http.StatusBadRequest, "missing subscription id")
		return
	}
	if h.srv.watchSvc == nil {
		writeError(w, http.StatusNotFound, "watch service not available")
		return
	}
	if !h.srv.watchSvc.Kill(id) {
		writeError(w, http.StatusNotFound, "subscription not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "killed"})
}

// ---------- GET /admin/api/cluster/nodes ----------

type clusterNodeInfo struct {
	Address  string `json:"address"`
	NodeID   string `json:"node_id"`
	Role     string `json:"role"`
	IsLeader bool   `json:"is_leader"`
	Status   string `json:"status"`
}

func (h *AdminHandler) clusterNodes(w http.ResponseWriter, r *http.Request) {
	if h.srv.node == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"mode":  "single",
			"nodes": []clusterNodeInfo{},
		})
		return
	}

	leaderID := h.srv.node.LeaderID()
	peers := h.srv.node.Peers()

	// Build address → nodeID mapping from peer_addresses config.
	// peer_addresses maps nodeID → gRPC addr (e.g. "node1:5051").
	// peers are Raft HTTP addresses (e.g. "http://node1:9090").
	// We extract the node ID by matching hostnames: "node1" appears in both.
	addrToNodeID := make(map[string]string)
	for nodeID, grpcAddr := range h.cfg.Get().PeerAddresses {
		host, _, _ := splitHostPort(grpcAddr)
		if host == "" {
			host = grpcAddr
		}
		for _, p := range peers {
			peerHost, _, _ := splitRaftHost(p)
			if peerHost == host {
				addrToNodeID[p] = nodeID
			}
		}
	}

	nodes := make([]clusterNodeInfo, 0, len(peers))
	for _, p := range peers {
		nid := addrToNodeID[p]
		if nid == "" {
			// Fallback: extract from address (e.g. "http://node1:9090" → "node1")
			if host, _, err := splitRaftHost(p); err == nil {
				nid = host
			}
		}
		nodes = append(nodes, clusterNodeInfo{
			Address:  p,
			NodeID:   nid,
			Role:     roleForNode(nid, leaderID),
			IsLeader: nid == leaderID,
			Status:   "ok",
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"mode":        "distributed",
		"nodes":       nodes,
		"leader_id":   leaderID,
		"peers_total": len(peers),
	})
}

func roleForNode(nodeID, leaderID string) string {
	if nodeID == leaderID {
		return "leader"
	}
	return "follower"
}

func splitRaftHost(addr string) (string, string, error) {
	// addr is like "http://node1:9090"
	s := addr
	if after, ok := strings.CutPrefix(s, "http://"); ok {
		s = after
	}
	if after, ok := strings.CutPrefix(s, "https://"); ok {
		s = after
	}
	return splitHostPort(s)
}

func splitHostPort(s string) (host, port string, err error) {
	colon := strings.LastIndex(s, ":")
	if colon < 0 {
		return s, "", nil
	}
	return s[:colon], s[colon+1:], nil
}

// ---------- GET /admin/api/raft ----------

func (h *AdminHandler) raftState(w http.ResponseWriter, r *http.Request) {
	if h.srv.node == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"mode": "single",
		})
		return
	}
	status := h.srv.node.Status()
	status["peers"] = h.srv.node.Peers()
	writeJSON(w, http.StatusOK, status)
}

// ---------- GET /admin/api/keys/stats ----------

func (h *AdminHandler) keysStats(w http.ResponseWriter, r *http.Request) {
	stats := h.srv.fsm.Cache.Stats()
	writeJSON(w, http.StatusOK, stats)
}

// ---------- GET /admin/api/watch (SSE) ----------

func (h *AdminHandler) watchSSE(w http.ResponseWriter, r *http.Request) {
	// Validate token from query param (EventSource doesn't support custom headers).
	if token := h.cfg.Get().AuthToken; token != "" {
		if r.URL.Query().Get("token") != token {
			writeError(w, http.StatusUnauthorized, "missing or invalid auth token")
			return
		}
	}

	if h.srv.watchSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "watch service not available")
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	pattern := r.URL.Query().Get("pattern")
	sub := h.srv.watchSvc.Subscribe(pattern)
	defer h.srv.watchSvc.Unsubscribe(sub)

	// Send initial connection event
	fmt.Fprintf(w, "event: connected\ndata: {\"id\":\"%s\"}\n\n", sub.ID)
	flusher.Flush()

	for {
		select {
		case evt, ok := <-sub.Ch:
			if !ok {
				return
			}
			data, _ := json.Marshal(evt)
			fmt.Fprintf(w, "event: message\ndata: %s\n\n", data)
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

// ---------- POST /admin/api/set (plain-value bridge) ----------

// adminSetRequest accepts plain JSON values and wraps them in protobuf Any
// before delegating to the gRPC-backed Set handler. This avoids requiring
// the frontend to know about protobuf Any @type encoding.
type adminSetRequest struct {
	Value  any    `json:"value"`
	Expire string `json:"expire,omitempty"`
}

func (h *AdminHandler) adminSet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// Extract key from path: /admin/api/set/<key>
	key := strings.TrimPrefix(r.URL.Path, "/admin/api/set/")
	if key == "" {
		writeError(w, http.StatusBadRequest, "missing key in path")
		return
	}

	var body adminSetRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Wrap the value in a protobuf Any.
	anyVal, err := utils.ConvertToAnyPB(body.Value)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("unsupported value type: %v", err))
		return
	}

	// Delegate to the existing gRPC-based Set handler via in-process call.
	ctx := r.Context()
	resp, err := h.srv.Set(ctx, &pb.SetRequest{
		Key:    key,
		Value:  anyVal,
		Expire: body.Expire,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// ---------- helper for atomic config access ----------

func (h *AdminHandler) Config() *config.Config {
	return h.cfg.Get()
}
