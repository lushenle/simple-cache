package controller

import (
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	reconcileTotal = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "simplecache_operator_reconcile_total",
			Help: "Total number of reconciliations",
		},
	)
	reconcileDuration = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "simplecache_operator_reconcile_duration_seconds",
			Help:    "Duration of reconciliations in seconds",
			Buckets: prometheus.DefBuckets,
		},
	)
	reconcileErrors = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "simplecache_operator_reconcile_errors_total",
			Help: "Total number of reconciliation errors",
		},
	)
	managedClusters = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "simplecache_operator_clusters_total",
			Help: "Number of managed cache clusters",
		},
	)
	managedNodes = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "simplecache_operator_nodes_total",
			Help: "Total number of managed cache nodes",
		},
	)
)

func init() {
	metrics.Registry.MustRegister(
		reconcileTotal,
		reconcileDuration,
		reconcileErrors,
		managedClusters,
		managedNodes,
	)
}
