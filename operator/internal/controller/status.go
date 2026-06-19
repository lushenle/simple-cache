package controller

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	cachev1 "github.com/lushenle/simple-cache/operator/api/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	conditionTypeAvailable   = "Available"
	conditionTypeProgressing = "Progressing"
	conditionTypeDegraded    = "Degraded"

	reasonClusterReady    = "ClusterReady"
	reasonClusterNotReady = "ClusterNotReady"
	reasonNoLeader        = "NoLeader"
	reasonRollingUpdate   = "RollingUpdate"
	reasonDegraded        = "NodesUnhealthy"
)

type healthzResponse struct {
	Status string `json:"status"`
	Role   string `json:"role,omitempty"`
	Ready  bool   `json:"ready,omitempty"`
}

func collectStatus(ctx context.Context, cr *cachev1.CacheCluster, k8sClient client.Client) (*cachev1.CacheClusterStatus, error) {
	lbls := labels(cr)
	svc := headlessServiceName(cr)
	ns := cr.Namespace

	status := &cachev1.CacheClusterStatus{
		Replicas:           cr.Spec.Replicas,
		ObservedGeneration: cr.Generation,
	}

	// List pods matching the cluster labels
	podList := &corev1.PodList{}
	if err := k8sClient.List(ctx, podList,
		client.InNamespace(ns),
		client.MatchingLabels(lbls),
	); err != nil {
		return status, fmt.Errorf("failed to list pods: %w", err)
	}

	var nodes []cachev1.NodeStatus
	var readyCount int32
	var leaderNodeId string

	for _, pod := range podList.Items {
		// Skip terminating pods
		if !pod.DeletionTimestamp.IsZero() {
			continue
		}

		nodeId := pod.Name
		grpcAddr := fmt.Sprintf("%s.%s.%s.%s:%d", pod.Name, svc, ns, clusterDomain, grpcPort)

		node := cachev1.NodeStatus{
			NodeId:   nodeId,
			PodName:  pod.Name,
			GrpcAddr: grpcAddr,
			Ready:    false,
			Role:     "unknown",
		}

		// Check pod phase
		if pod.Status.Phase != corev1.PodRunning {
			nodes = append(nodes, node)
			continue
		}

		// Try to query healthz endpoint for role/leader info
		health := probeHealth(ctx, pod.Status.PodIP, cr.Spec.TLS.Enabled)
		node.Ready = health.Ready
		if health.Role != "" {
			node.Role = health.Role
		}

		if node.Ready {
			readyCount++
			if node.Role == "leader" {
				leaderNodeId = nodeId
			}
		}

		nodes = append(nodes, node)
	}

	status.Nodes = nodes
	status.ReadyReplicas = readyCount

	if leaderNodeId != "" {
		for _, n := range nodes {
			if n.NodeId == leaderNodeId {
				status.Leader = &cachev1.LeaderStatus{
					NodeId:  leaderNodeId,
					PodName: n.PodName,
				}
				break
			}
		}
	}

	// Check StatefulSet update progress
	progressingFromSts := false
	sts := &appsv1.StatefulSet{}
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: cr.Name, Namespace: cr.Namespace}, sts); err == nil {
		if sts.Status.UpdateRevision != "" && sts.Status.UpdateRevision != sts.Status.CurrentRevision {
			progressingFromSts = true
		}
	}

	// Build conditions, preserving LastTransitionTime when status hasn't changed
	status.Conditions = buildConditions(cr.Status.Conditions, status, cr.Generation, progressingFromSts)

	return status, nil
}

func probeHealth(ctx context.Context, podIP string, useTLS bool) healthzResponse {
	logger := log.FromContext(ctx)

	if podIP == "" {
		return healthzResponse{}
	}

	scheme := "http"
	if useTLS {
		scheme = "https"
	}
	url := fmt.Sprintf("%s://%s:%d/healthz", scheme, podIP, httpPort)
	reqCtx, cancel := context.WithTimeout(ctx, healthProbeTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		logger.V(1).Info("health probe request error", "url", url, "error", err)
		return healthzResponse{}
	}

	httpClient := &http.Client{Timeout: healthProbeTimeout}
	if useTLS {
		httpClient.Transport = &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		}
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		logger.V(1).Info("health probe failed", "url", url, "error", err)
		return healthzResponse{}
	}
	defer resp.Body.Close()

	var h healthzResponse
	if err := json.NewDecoder(resp.Body).Decode(&h); err != nil {
		logger.V(1).Info("health probe decode error", "url", url, "error", err)
		return healthzResponse{}
	}
	return h
}

func buildConditions(existing []metav1.Condition, status *cachev1.CacheClusterStatus, generation int64, progressingFromSts bool) []metav1.Condition {
	replicas := status.Replicas
	readyReplicas := status.ReadyReplicas

	// --- Available ---
	availableStatus := metav1.ConditionTrue
	availableReason := reasonClusterReady
	availableMsg := fmt.Sprintf("%d/%d nodes ready", readyReplicas, replicas)

	if readyReplicas < replicas {
		availableStatus = metav1.ConditionFalse
		availableReason = reasonClusterNotReady
	}
	if replicas > 1 && status.Leader == nil {
		availableStatus = metav1.ConditionFalse
		availableReason = reasonNoLeader
		availableMsg = "no leader elected"
	}
	if status.Leader != nil {
		availableMsg += fmt.Sprintf(", leader: %s", status.Leader.PodName)
	}

	available := newCondition(conditionTypeAvailable, availableStatus, availableReason, availableMsg, existing, generation)

	// --- Progressing ---
	// True when: a rolling update is in progress (StatefulSet revision mismatch)
	// or when some pods haven't reached the desired state.
	progressingStatus := metav1.ConditionFalse
	progressingReason := "Stable"
	progressingMsg := "cluster is stable"

	if progressingFromSts {
		progressingStatus = metav1.ConditionTrue
		progressingReason = reasonRollingUpdate
		progressingMsg = "StatefulSet rolling update in progress"
	} else if readyReplicas < replicas {
		progressingStatus = metav1.ConditionTrue
		progressingReason = reasonRollingUpdate
		progressingMsg = fmt.Sprintf("waiting for %d pods to be ready", replicas-readyReplicas)
	}

	progressing := newCondition(conditionTypeProgressing, progressingStatus, progressingReason, progressingMsg, existing, generation)

	// --- Degraded ---
	// Only mark Degraded when no pods are ready. During initial rollout (readyReplicas=0),
	// the Progressing condition already captures this state.
	degradedStatus := metav1.ConditionFalse
	degradedReason := "Healthy"
	degradedMsg := "all nodes healthy"

	if replicas > 0 && readyReplicas == 0 {
		// Check existing Degraded condition: only trigger after being in this state for 2+ minutes
		prevDegraded := findCondition(conditionTypeDegraded, existing)
		if prevDegraded != nil && prevDegraded.Status == metav1.ConditionTrue {
			// Already degraded, keep it
			degradedStatus = metav1.ConditionTrue
			degradedReason = reasonDegraded
			degradedMsg = fmt.Sprintf("%d/%d nodes unhealthy", replicas, replicas)
		} else if prevDegraded != nil {
			// Check how long we've been in this state (use LastTransitionTime as gauge)
			timeSinceTransition := time.Since(prevDegraded.LastTransitionTime.Time)
			if timeSinceTransition > degradedHysteresis {
				degradedStatus = metav1.ConditionTrue
				degradedReason = reasonDegraded
				degradedMsg = fmt.Sprintf("no ready pods for %v", timeSinceTransition.Truncate(time.Second))
			}
		}
		// If no previous Degraded condition exists (initial deploy), remain False — Progressing handles it
	}

	degraded := newCondition(conditionTypeDegraded, degradedStatus, degradedReason, degradedMsg, existing, generation)

	return []metav1.Condition{available, progressing, degraded}
}

// newCondition creates a metav1.Condition, preserving LastTransitionTime when
// the status matches the previous observation.
func newCondition(condType string, status metav1.ConditionStatus, reason, message string, existing []metav1.Condition, generation int64) metav1.Condition {
	now := metav1.Now()
	transitionTime := now

	previous := findCondition(condType, existing)
	if previous != nil && previous.Status == status && previous.Reason == reason {
		transitionTime = previous.LastTransitionTime
	}

	return metav1.Condition{
		Type:               condType,
		Status:             status,
		LastTransitionTime: transitionTime,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: generation,
	}
}

func findCondition(condType string, conditions []metav1.Condition) *metav1.Condition {
	for i := range conditions {
		if conditions[i].Type == condType {
			return &conditions[i]
		}
	}
	return nil
}
