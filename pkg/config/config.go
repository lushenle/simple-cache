package config

import (
	"fmt"
	"net"
	"os"
	"sync/atomic"
	"time"

	"github.com/lushenle/simple-cache/pkg/common"
	"go.uber.org/zap"
	"gopkg.in/yaml.v3"
)

type Config struct {
	Mode              common.Mode       `yaml:"mode"`
	NodeID            string            `yaml:"node_id"`
	GRPCAddr          string            `yaml:"grpc_addr"`
	HTTPAddr          string            `yaml:"http_addr"`
	RaftHTTPAddr      string            `yaml:"raft_http_addr"`
	MetricsAddr       string            `yaml:"metrics_addr"`
	Peers             []string          `yaml:"peers"`
	PeerAddresses     map[string]string `yaml:"peer_addresses"` // nodeID → gRPC addr (Phase 2 leader discovery)
	HeartbeatMS       int               `yaml:"heartbeat_ms"`
	ElectionMS        int               `yaml:"election_ms"`
	HotReload         bool              `yaml:"hot_reload"`
	LoadOnStartup     bool              `yaml:"load_on_startup"`
	DumpOnShutdown    bool              `yaml:"dump_on_shutdown"`
	DumpFormat        common.DumpFormat `yaml:"dump_format"`
	DataDir           string            `yaml:"data_dir"`
	AuthToken         string            `yaml:"auth_token"`
	EnableTLS         bool              `yaml:"enable_tls"`
	TLSCertFile       string            `yaml:"tls_cert_file"`
	TLSKeyFile        string            `yaml:"tls_key_file"`
	AllowedOrigins    []string          `yaml:"allowed_origins"`
	SnapshotEnabled   bool              `yaml:"snapshot_enabled"`
	SnapshotThreshold uint64            `yaml:"snapshot_threshold"`
	MaxKeys           int               `yaml:"max_keys"`            // max cache keys (0 = unlimited)
	MaxValueSize      int               `yaml:"max_value_size"`      // max value size in bytes (0 = unlimited)
	MaxQPS            int               `yaml:"max_qps"`             // max requests/sec per client (0 = unlimited)
}

func Default() *Config {
	return &Config{
		Mode:              common.ModeSingle,
		NodeID:            "node-1",
		GRPCAddr:          ":5051",
		HTTPAddr:          ":8080",
		RaftHTTPAddr:      ":9090",
		MetricsAddr:       ":2112",
		HeartbeatMS:       200,
		ElectionMS:        1200,
		HotReload:         false,
		LoadOnStartup:     true,
		DumpOnShutdown:    true,
		DumpFormat:        common.DumpFormatBinary,
		DataDir:           "data",
		AllowedOrigins:    nil,
		SnapshotEnabled:   true,
		SnapshotThreshold: 1024,
	}
}

func Load(path string) (*Config, error) {
	d := Default()
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return d, nil
		}
		return nil, err
	}
	if err := yaml.Unmarshal(b, d); err != nil {
		return nil, err
	}
	return d, nil
}

// Validate checks the configuration for common errors.
func (c *Config) OverrideFromEnv() {
	if v := os.Getenv("SIMPLE_CACHE_MODE"); v != "" {
		c.Mode = common.Mode(v)
	}
	if v := os.Getenv("SIMPLE_CACHE_NODE_ID"); v != "" {
		c.NodeID = v
	}
	if v := os.Getenv("SIMPLE_CACHE_GRPC_ADDR"); v != "" {
		c.GRPCAddr = v
	}
	if v := os.Getenv("SIMPLE_CACHE_HTTP_ADDR"); v != "" {
		c.HTTPAddr = v
	}
	if v := os.Getenv("SIMPLE_CACHE_RAFT_HTTP_ADDR"); v != "" {
		c.RaftHTTPAddr = v
	}
	if v := os.Getenv("SIMPLE_CACHE_METRICS_ADDR"); v != "" {
		c.MetricsAddr = v
	}
	if v := os.Getenv("SIMPLE_CACHE_AUTH_TOKEN"); v != "" {
		c.AuthToken = v
	}
	if v := os.Getenv("SIMPLE_CACHE_DATA_DIR"); v != "" {
		c.DataDir = v
	}
	if v := os.Getenv("SIMPLE_CACHE_MAX_KEYS"); v != "" {
		if n, err := fmt.Sscanf(v, "%d", &c.MaxKeys); err == nil && n == 1 {
		}
	}
	if v := os.Getenv("SIMPLE_CACHE_MAX_VALUE_SIZE"); v != "" {
		if n, err := fmt.Sscanf(v, "%d", &c.MaxValueSize); err == nil && n == 1 {
		}
	}
	if v := os.Getenv("SIMPLE_CACHE_MAX_QPS"); v != "" {
		if n, err := fmt.Sscanf(v, "%d", &c.MaxQPS); err == nil && n == 1 {
		}
	}
	if v := os.Getenv("SIMPLE_CACHE_HEARTBEAT_MS"); v != "" {
		if n, err := fmt.Sscanf(v, "%d", &c.HeartbeatMS); err == nil && n == 1 {
		}
	}
	if v := os.Getenv("SIMPLE_CACHE_ELECTION_MS"); v != "" {
		if n, err := fmt.Sscanf(v, "%d", &c.ElectionMS); err == nil && n == 1 {
		}
	}
}

func (c *Config) Validate() error {
	switch c.Mode {
	case common.ModeSingle, common.ModeDistributed:
	default:
		return fmt.Errorf("invalid mode: %q (expected 'single' or 'distributed')", c.Mode)
	}
	if c.NodeID == "" {
		return fmt.Errorf("node_id must not be empty")
	}
	for name, addr := range map[string]string{
		"grpc_addr":     c.GRPCAddr,
		"http_addr":     c.HTTPAddr,
		"raft_http_addr": c.RaftHTTPAddr,
		"metrics_addr":  c.MetricsAddr,
	} {
		if addr == "" {
			return fmt.Errorf("%s must not be empty", name)
		}
		if _, err := net.ResolveTCPAddr("tcp", addr); err != nil {
			return fmt.Errorf("%s: invalid address: %w", name, err)
		}
	}
	if c.Mode.IsDistributed() && len(c.Peers) == 0 {
		return fmt.Errorf("peers must not be empty in distributed mode")
	}
	if c.HeartbeatMS <= 0 {
		return fmt.Errorf("heartbeat_ms must be positive")
	}
	if c.ElectionMS <= c.HeartbeatMS {
		return fmt.Errorf("election_ms (%d) must be greater than heartbeat_ms (%d)", c.ElectionMS, c.HeartbeatMS)
	}
	if c.SnapshotEnabled && c.SnapshotThreshold == 0 {
		return fmt.Errorf("snapshot_threshold must be > 0 when snapshot_enabled is true")
	}
	if c.EnableTLS {
		if c.TLSCertFile == "" {
			return fmt.Errorf("tls_cert_file must not be empty when enable_tls is true")
		}
		if c.TLSKeyFile == "" {
			return fmt.Errorf("tls_key_file must not be empty when enable_tls is true")
		}
	}
	if c.MaxKeys < 0 {
		return fmt.Errorf("max_keys must not be negative")
	}
	if c.MaxValueSize < 0 {
		return fmt.Errorf("max_value_size must not be negative")
	}
	if c.MaxQPS < 0 {
		return fmt.Errorf("max_qps must not be negative")
	}
	return nil
}

type AtomicConfig struct {
	v atomic.Value
}

func NewAtomic(cfg *Config) *AtomicConfig {
	a := &AtomicConfig{}
	a.v.Store(cfg)
	return a
}

func (a *AtomicConfig) Get() *Config {
	return a.v.Load().(*Config)
}

func (a *AtomicConfig) Set(c *Config) {
	a.v.Store(c)
}

type Watcher struct {
	path   string
	out    *AtomicConfig
	logger *zap.Logger
}

func NewWatcher(path string, out *AtomicConfig) *Watcher {
	return &Watcher{path: path, out: out, logger: zap.NewNop()}
}

// SetLogger sets a logger for the watcher.
func (w *Watcher) SetLogger(logger *zap.Logger) {
	if logger != nil {
		w.logger = logger
	}
}

func (w *Watcher) Start(stop <-chan struct{}) {
	if w.out == nil || w.path == "" {
		return
	}
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	var last []byte
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			b, err := os.ReadFile(w.path)
			if err != nil {
				continue
			}
			if string(b) == string(last) {
				continue
			}
			cfg := &Config{}
			if err := yaml.Unmarshal(b, cfg); err != nil {
				w.logger.Warn("failed to parse config file, skipping hot reload", zap.Error(err), zap.String("path", w.path))
				continue
			}
			w.out.Set(cfg)
			last = b
			w.logger.Info("config reloaded", zap.String("path", w.path))
		}
	}
}
