/*
Copyright 2026 Shenle Lu.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// CacheConfig defines cache-specific settings.
type CacheConfig struct {
	// MaxKeys is the maximum number of keys allowed. 0 means unlimited.
	// +kubebuilder:validation:Minimum=0
	MaxKeys int `json:"maxKeys,omitempty"`

	// MaxValueSize is the maximum value size in bytes. 0 means unlimited.
	// +kubebuilder:validation:Minimum=0
	MaxValueSize int `json:"maxValueSize,omitempty"`

	// EvictionPolicy is the eviction policy: none | lru.
	// +kubebuilder:validation:Enum=none;lru
	EvictionPolicy string `json:"evictionPolicy,omitempty"`

	// MaxQPS is the maximum queries per second. 0 means unlimited.
	// +kubebuilder:validation:Minimum=0
	MaxQPS int `json:"maxQPS,omitempty"`
}

// RaftConfig defines Raft consensus settings.
type RaftConfig struct {
	// HeartbeatMs is the Raft heartbeat interval in milliseconds.
	HeartbeatMs int `json:"heartbeatMs,omitempty"`

	// ElectionMs is the Raft leader election timeout in milliseconds.
	ElectionMs int `json:"electionMs,omitempty"`

	// SnapshotEnabled enables Raft snapshotting.
	SnapshotEnabled bool `json:"snapshotEnabled,omitempty"`

	// SnapshotThreshold is the number of log entries before triggering a snapshot.
	SnapshotThreshold int `json:"snapshotThreshold,omitempty"`
}

// PersistenceConfig defines persistence settings.
type PersistenceConfig struct {
	// DumpOnShutdown dumps cache data to disk on shutdown.
	DumpOnShutdown bool `json:"dumpOnShutdown,omitempty"`

	// LoadOnStartup loads cache data from disk on startup.
	LoadOnStartup bool `json:"loadOnStartup,omitempty"`

	// DumpFormat is the serialization format: binary | json.
	// +kubebuilder:validation:Enum=binary;json
	DumpFormat string `json:"dumpFormat,omitempty"`

	// DataDir is the directory for persistent data.
	DataDir string `json:"dataDir,omitempty"`
}

// StorageConfig defines persistent storage settings.
type StorageConfig struct {
	// Size is the size of the persistent volume claim (e.g. "10Gi", "500Mi").
	// +kubebuilder:validation:Pattern=`^[0-9]+(\.[0-9]+)?(Ki|Mi|Gi|Ti|Pi|Ei|m|k|M|G|T|P|E)$`
	Size string `json:"size"`

	// StorageClassName is the StorageClass name. Empty uses the default.
	StorageClassName string `json:"storageClassName,omitempty"`
}

// AuthConfig defines authentication settings.
type AuthConfig struct {
	// Enabled toggles authentication.
	Enabled bool `json:"enabled"`

	// TokenSecretRef references a Secret containing the auth token.
	TokenSecretRef corev1.SecretKeySelector `json:"tokenSecretRef,omitempty"`
}

// TLSConfig defines TLS settings.
type TLSConfig struct {
	// Enabled toggles TLS.
	Enabled bool `json:"enabled"`

	// CertSecretRef references a Secret containing the TLS certificate.
	CertSecretRef corev1.LocalObjectReference `json:"certSecretRef,omitempty"`

	// CertKey is the key in the Secret for the certificate file.
	CertKey string `json:"certKey,omitempty"`

	// KeyKey is the key in the Secret for the private key file.
	KeyKey string `json:"keyKey,omitempty"`
}

// ServiceMonitorConfig defines ServiceMonitor settings.
type ServiceMonitorConfig struct {
	// Enabled toggles creation of a ServiceMonitor for Prometheus.
	Enabled bool `json:"enabled,omitempty"`

	// Interval is the scrape interval.
	Interval string `json:"interval,omitempty"`

	// Labels to add to the ServiceMonitor.
	Labels map[string]string `json:"labels,omitempty"`
}

// MonitoringConfig defines monitoring settings.
type MonitoringConfig struct {
	// ServiceMonitor configures a Prometheus ServiceMonitor.
	ServiceMonitor ServiceMonitorConfig `json:"serviceMonitor,omitempty"`
}

// CacheClusterSpec defines the desired state of CacheCluster.
// +kubebuilder:validation:XValidation:rule="self.replicas >= 1 && (self.replicas == 1 || self.replicas % 2 == 1)",message="replicas must be >= 1 and odd for Raft consensus"
type CacheClusterSpec struct {
	// Replicas is the desired number of replicas. Must be >= 1 and odd for Raft consensus.
	// +kubebuilder:validation:Minimum=1
	Replicas int32 `json:"replicas"`

	// Image is the container image for simple-cache.
	Image string `json:"image,omitempty"`

	// ImagePullPolicy is the container image pull policy.
	// +kubebuilder:validation:Enum=Always;Never;IfNotPresent
	ImagePullPolicy corev1.PullPolicy `json:"imagePullPolicy,omitempty"`

	// Cache contains cache-specific configuration.
	Cache CacheConfig `json:"cache,omitempty"`

	// Raft contains Raft consensus configuration.
	Raft RaftConfig `json:"raft,omitempty"`

	// Persistence contains persistence configuration.
	Persistence PersistenceConfig `json:"persistence,omitempty"`

	// Storage contains persistent storage configuration.
	Storage StorageConfig `json:"storage,omitempty"`

	// Resources is the compute resource requirements.
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`

	// Auth contains authentication configuration.
	Auth AuthConfig `json:"auth,omitempty"`

	// TLS contains TLS configuration.
	TLS TLSConfig `json:"tls,omitempty"`

	// Monitoring contains monitoring configuration.
	Monitoring MonitoringConfig `json:"monitoring,omitempty"`

	// Affinity is the pod scheduling affinity.
	Affinity *corev1.Affinity `json:"affinity,omitempty"`

	// Tolerations is the pod scheduling tolerations.
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`

	// NodeSelector is the pod node selector.
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`

	// TopologySpreadConstraints is the pod topology spread constraints.
	TopologySpreadConstraints []corev1.TopologySpreadConstraint `json:"topologySpreadConstraints,omitempty"`

	// TerminationGracePeriodSeconds is the pod termination grace period.
	// +kubebuilder:validation:Minimum=0
	TerminationGracePeriodSeconds *int64 `json:"terminationGracePeriodSeconds,omitempty"`

	// ExtraEnv contains additional environment variables.
	ExtraEnv []corev1.EnvVar `json:"extraEnv,omitempty"`
}

// LeaderStatus identifies the current Raft leader.
type LeaderStatus struct {
	// NodeId is the node ID of the leader.
	NodeId string `json:"nodeId,omitempty"`

	// PodName is the pod name of the leader.
	PodName string `json:"podName,omitempty"`
}

// NodeStatus describes an individual cluster node.
type NodeStatus struct {
	// NodeId is the node ID (e.g. my-cache-0).
	NodeId string `json:"nodeId,omitempty"`

	// PodName is the pod name.
	PodName string `json:"podName,omitempty"`

	// Role is the node's role: leader | follower.
	Role string `json:"role,omitempty"`

	// GrpcAddr is the gRPC address of the node.
	GrpcAddr string `json:"grpcAddr,omitempty"`

	// Ready indicates whether the node is ready.
	Ready bool `json:"ready"`
}

// CacheClusterStatus defines the observed state of CacheCluster.
type CacheClusterStatus struct {
	// Conditions represent the current state of the cluster.
	// +optional
	// +patchMergeKey=type
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`

	// Leader identifies the current Raft leader.
	Leader *LeaderStatus `json:"leader,omitempty"`

	// Nodes lists all cluster nodes and their status.
	Nodes []NodeStatus `json:"nodes,omitempty"`

	// Replicas is the desired number of replicas.
	Replicas int32 `json:"replicas,omitempty"`

	// ReadyReplicas is the number of ready replicas.
	ReadyReplicas int32 `json:"readyReplicas,omitempty"`

	// ObservedGeneration is the generation observed by the controller.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// GetConditions returns the conditions list (for conditions.Getter interface).
func (s *CacheClusterStatus) GetConditions() []metav1.Condition {
	return s.Conditions
}

// SetConditions sets the conditions list (for conditions.Setter interface).
func (s *CacheClusterStatus) SetConditions(conditions []metav1.Condition) {
	s.Conditions = conditions
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Replicas",type="integer",JSONPath=".spec.replicas",description="Desired replicas"
// +kubebuilder:printcolumn:name="Ready",type="integer",JSONPath=".status.readyReplicas",description="Ready replicas"
// +kubebuilder:printcolumn:name="Leader",type="string",JSONPath=".status.leader.nodeId",description="Current leader"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// CacheCluster is the Schema for the cacheclusters API.
type CacheCluster struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   CacheClusterSpec   `json:"spec,omitempty"`
	Status CacheClusterStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// CacheClusterList contains a list of CacheCluster.
type CacheClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []CacheCluster `json:"items"`
}

func init() {
	SchemeBuilder.Register(&CacheCluster{}, &CacheClusterList{})
}
