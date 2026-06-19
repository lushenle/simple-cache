package controller

import (
	"fmt"

	cachev1 "github.com/lushenle/simple-cache/operator/api/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func buildConfigMap(cr *cachev1.CacheCluster) *corev1.ConfigMap {
	lbls := labels(cr)
	ns := cr.Namespace

	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      configMapName(cr),
			Namespace: ns,
			Labels:    lbls,
		},
		Data: map[string]string{
			"init.sh":                      buildInitScript(cr),
			"cache.max_keys":               fmt.Sprintf("%d", cr.Spec.Cache.MaxKeys),
			"cache.max_value_size":         fmt.Sprintf("%d", cr.Spec.Cache.MaxValueSize),
			"cache.eviction_policy":        cr.Spec.Cache.EvictionPolicy,
			"cache.max_qps":                fmt.Sprintf("%d", cr.Spec.Cache.MaxQPS),
			"raft.heartbeat_ms":            fmt.Sprintf("%d", cr.Spec.Raft.HeartbeatMs),
			"raft.election_ms":             fmt.Sprintf("%d", cr.Spec.Raft.ElectionMs),
			"raft.snapshot_enabled":        fmt.Sprintf("%t", cr.Spec.Raft.SnapshotEnabled),
			"raft.snapshot_threshold":      fmt.Sprintf("%d", cr.Spec.Raft.SnapshotThreshold),
			"persistence.dump_on_shutdown": fmt.Sprintf("%t", cr.Spec.Persistence.DumpOnShutdown),
			"persistence.load_on_startup":  fmt.Sprintf("%t", cr.Spec.Persistence.LoadOnStartup),
			"persistence.dump_format":      cr.Spec.Persistence.DumpFormat,
			"persistence.data_dir":         cr.Spec.Persistence.DataDir,
		},
	}
}

func buildInitScript(cr *cachev1.CacheCluster) string {
	replicas := int(cr.Spec.Replicas)
	svc := headlessServiceName(cr)
	ns := cr.Namespace
	domain := fmt.Sprintf("%s.%s.%s", svc, ns, clusterDomain)

	heartbeatMs := cr.Spec.Raft.HeartbeatMs
	electionMs := cr.Spec.Raft.ElectionMs
	snapshotEnabled := cr.Spec.Raft.SnapshotEnabled
	snapshotThreshold := cr.Spec.Raft.SnapshotThreshold
	maxKeys := cr.Spec.Cache.MaxKeys
	maxValueSize := cr.Spec.Cache.MaxValueSize
	evictionPolicy := cr.Spec.Cache.EvictionPolicy
	maxQPS := cr.Spec.Cache.MaxQPS
	dumpOnShutdown := cr.Spec.Persistence.DumpOnShutdown
	loadOnStartup := cr.Spec.Persistence.LoadOnStartup
	dumpFormat := cr.Spec.Persistence.DumpFormat
	dataDir := cr.Spec.Persistence.DataDir
	if dataDir == "" {
		dataDir = defaultDataDir
	}

	// fmt1 contains replicas, domain, ports, paths — everything used before the YAMLEOF heredoc body.
	mode := "distributed"
	if replicas == 1 {
		mode = "single"
	}
	fmt1 := fmt.Sprintf(
		"#!/bin/sh\n"+
			"set -e\n"+
			"\n"+
			"REPLICAS=%d\n"+
			"SERVICE=\"%s\"\n"+
			"NODE_INDEX=${HOSTNAME##*-}\n"+
			"\n"+
			"cat > %s <<YAMLEOF\n"+
			"mode: %s\n"+
			"node_id: ${HOSTNAME}\n"+
			"grpc_addr: \":%d\"\n"+
			"http_addr: \":%d\"\n"+
			"raft_http_addr: \":%d\"\n"+
			"metrics_addr: \":%d\"\n"+
			"data_dir: %s\n",
		replicas, domain, configFilePath, mode, grpcPort, httpPort, raftPort, metricsPort, dataDir)

	// Build peer lists using command substitution ($(for...echo)) to produce
	// real newlines — shell variable concatenation with \n would be literal.
	if replicas > 1 {
		fmt1 += fmt.Sprintf(
			"peers:\n"+
				"$(for i in $(seq 0 $((REPLICAS-1))); do\n"+
				"  echo \"  - http://${SERVICE%%.*.*}-${i}.${SERVICE}:%d\"\n"+
				"done)\n"+
				"peer_addresses:\n"+
				"$(for i in $(seq 0 $((REPLICAS-1))); do\n"+
				"  echo \"    ${SERVICE%%.*.*}-${i}: \\\"${SERVICE%%.*.*}-${i}.${SERVICE}:%d\\\"\"\n"+
				"done)\n",
			raftPort, grpcPort)
	} else {
		fmt1 += "peers: []\n" +
			"peer_addresses: {}\n"
	}

	fmt1 += fmt.Sprintf(
		"heartbeat_ms: %d\n"+
			"election_ms: %d\n"+
			"snapshot_enabled: %t\n"+
			"snapshot_threshold: %d\n"+
			"max_keys: %d\n"+
			"max_value_size: %d\n"+
			"eviction_policy: %s\n"+
			"max_qps: %d\n"+
			"dump_on_shutdown: %t\n"+
			"load_on_startup: %t\n"+
			"dump_format: %s\n"+
			"YAMLEOF\n"+
			"\n"+
			"exec simple-cache\n",
		heartbeatMs, electionMs, snapshotEnabled, snapshotThreshold,
		maxKeys, maxValueSize, evictionPolicy, maxQPS,
		dumpOnShutdown, loadOnStartup, dumpFormat)

	return fmt1
}
