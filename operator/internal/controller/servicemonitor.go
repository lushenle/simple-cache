package controller

import (
	cachev1 "github.com/lushenle/simple-cache/operator/api/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var serviceMonitorGVK = schema.GroupVersionKind{
	Group:   "monitoring.coreos.com",
	Version: "v1",
	Kind:    "ServiceMonitor",
}

// buildServiceMonitor creates an unstructured ServiceMonitor resource.
// Returns nil if the monitoring operator is not installed (handled silently by createOrUpdate).
func buildServiceMonitor(cr *cachev1.CacheCluster) *unstructured.Unstructured {
	if !cr.Spec.Monitoring.ServiceMonitor.Enabled {
		return nil
	}

	lbls := labels(cr)
	interval := cr.Spec.Monitoring.ServiceMonitor.Interval
	if interval == "" {
		interval = "30s"
	}

	sm := &unstructured.Unstructured{}
	sm.SetGroupVersionKind(serviceMonitorGVK)
	sm.SetName(cr.Name)
	sm.SetNamespace(cr.Namespace)
	sm.SetLabels(lbls)

	unstructured.SetNestedField(sm.Object, lbls, "spec", "selector", "matchLabels")
	unstructured.SetNestedSlice(sm.Object, []interface{}{
		map[string]interface{}{
			"port":     "metrics",
			"interval": interval,
		},
	}, "spec", "endpoints")

	return sm
}
