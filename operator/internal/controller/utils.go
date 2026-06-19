package controller

import (
	"time"

	"k8s.io/apimachinery/pkg/util/intstr"
)

// --- Resource naming ---
const (
	// Label keys
	labelAppName      = "app.kubernetes.io/name"
	labelAppInstance  = "app.kubernetes.io/instance"
	labelAppComponent = "app.kubernetes.io/component"

	// Component values
	componentCacheNode = "cache-node"

	// App name
	appName = "simple-cache"
)

// --- Ports ---
const (
	grpcPort    = 5051
	httpPort    = 8080
	raftPort    = 9090
	metricsPort = 2112
)

// --- Paths ---
const (
	configMountPath   = "/init"
	configFileName    = "init.sh"
	configFilePath    = "/data/config.yaml"
	tlsCertsMountPath = "/etc/simple-cache/tls"
	defaultDataDir    = "/data"
)

// --- Defaults ---
const (
	defaultImage                  = "registry.shenle.lu/ishenle/simple-cache:latest"
	defaultRunAsUser              = int64(65532)
	defaultTerminationGracePeriod = int64(60)
	configMapFileMode             = int32(0o755)
)

// --- Probe timings ---
const (
	livenessInitialDelay  = int32(10)
	livenessPeriod        = int32(10)
	readinessInitialDelay = int32(5)
	readinessPeriod       = int32(5)
)

// --- Status ---
const (
	statusRetryAttempts = 3
	statusRetryDelay    = 100 * time.Millisecond
	healthProbeTimeout  = 2 * time.Second
	degradedHysteresis  = 2 * time.Minute
)

// --- DNS ---
// clusterDomain is the Kubernetes cluster-internal DNS suffix.
// TODO: make this configurable via a command-line flag for clusters with custom domains.
const clusterDomain = "svc.cluster.local"

func intstrPtr(i int32) intstr.IntOrString {
	return intstr.FromInt(int(i))
}
