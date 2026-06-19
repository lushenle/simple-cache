package controller

import (
	cachev1 "github.com/lushenle/simple-cache/operator/api/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// labels returns the common labels for all resources.
func labels(cr *cachev1.CacheCluster) map[string]string {
	return map[string]string{
		labelAppName:      appName,
		labelAppInstance:  cr.Name,
		labelAppComponent: componentCacheNode,
	}
}

// headlessServiceName returns the name of the headless service (same as CR name).
func headlessServiceName(cr *cachev1.CacheCluster) string {
	return cr.Name
}

// clientServiceName returns the name of the client service.
func clientServiceName(cr *cachev1.CacheCluster) string {
	return cr.Name + "-client"
}

// configMapName returns the name of the ConfigMap.
func configMapName(cr *cachev1.CacheCluster) string {
	return cr.Name
}

// statefulSetName returns the name of the StatefulSet.
func statefulSetName(cr *cachev1.CacheCluster) string {
	return cr.Name
}

func buildHeadlessService(cr *cachev1.CacheCluster) *corev1.Service {
	lbls := labels(cr)
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      headlessServiceName(cr),
			Namespace: cr.Namespace,
			Labels:    lbls,
		},
		Spec: corev1.ServiceSpec{
			ClusterIP:                corev1.ClusterIPNone,
			PublishNotReadyAddresses: true,
			Ports: []corev1.ServicePort{
				{Name: "grpc", Port: grpcPort, TargetPort: intstrPtr(grpcPort)},
				{Name: "http", Port: httpPort, TargetPort: intstrPtr(httpPort)},
				{Name: "raft", Port: raftPort, TargetPort: intstrPtr(raftPort)},
				{Name: "metrics", Port: metricsPort, TargetPort: intstrPtr(metricsPort)},
			},
			Selector: lbls,
		},
	}
}

func buildClientService(cr *cachev1.CacheCluster) *corev1.Service {
	lbls := labels(cr)
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clientServiceName(cr),
			Namespace: cr.Namespace,
			Labels:    lbls,
		},
		Spec: corev1.ServiceSpec{
			Type:                     corev1.ServiceTypeClusterIP,
			PublishNotReadyAddresses: true,
			Ports: []corev1.ServicePort{
				{Name: "grpc", Port: grpcPort, TargetPort: intstrPtr(grpcPort)},
				{Name: "http", Port: httpPort, TargetPort: intstrPtr(httpPort)},
			},
			Selector: lbls,
		},
	}
}
