package config

import (
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
		return d, nil
	}
	if err := yaml.Unmarshal(b, d); err != nil {
		return nil, err
	}
	return d, nil
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
