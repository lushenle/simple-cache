package config

import (
	"os"
	"sync/atomic"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Mode         string   `yaml:"mode"`
	NodeID       string   `yaml:"node_id"`
	GRPCAddr     string   `yaml:"grpc_addr"`
	HTTPAddr     string   `yaml:"http_addr"`
	RaftHTTPAddr string   `yaml:"raft_http_addr"`
	Peers        []string `yaml:"peers"`
	HeartbeatMS  int      `yaml:"heartbeat_ms"`
	ElectionMS   int      `yaml:"election_ms"`
	HotReload    bool     `yaml:"hot_reload"`
}

func Default() *Config {
	return &Config{
		Mode:         "single",
		NodeID:       "node-1",
		GRPCAddr:     ":5051",
		HTTPAddr:     ":8080",
		RaftHTTPAddr: ":9090",
		HeartbeatMS:  200,
		ElectionMS:   1200,
		HotReload:    false,
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
	path string
	out  *AtomicConfig
}

func NewWatcher(path string, out *AtomicConfig) *Watcher {
	return &Watcher{path: path, out: out}
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
				continue
			}
			w.out.Set(cfg)
			last = b
		}
	}
}
