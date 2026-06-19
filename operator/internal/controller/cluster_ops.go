package controller

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func ignoreTLSClient() *http.Client {
	return &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
}

// leaderPodIP probes each running pod's /healthz endpoint and returns the IP of
// the current Raft leader. Returns empty string if no leader is found.
func leaderPodIP(ctx context.Context, k8sClient client.Client, ns string, pods []corev1.Pod, useTLS bool) string {
	for _, pod := range pods {
		if pod.Status.Phase != corev1.PodRunning || pod.Status.PodIP == "" {
			continue
		}
		health := probeHealth(ctx, pod.Status.PodIP, useTLS)
		if health.Role == "leader" {
			return pod.Status.PodIP
		}
	}
	return ""
}

// removeNodeFromRaft calls POST /cluster/leave on the leader's HTTP port to
// gracefully remove a node from the Raft cluster before the pod is terminated.
func removeNodeFromRaft(ctx context.Context, leaderIP string, raftAddr string, useTLS bool) error {
	payload := map[string]string{"addr": raftAddr}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal leave payload: %w", err)
	}

	scheme := "http"
	if useTLS {
		scheme = "https"
	}
	url := fmt.Sprintf("%s://%s:%d/cluster/leave", scheme, leaderIP, httpPort)

	reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create leave request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 5 * time.Second}
	if useTLS {
		client = ignoreTLSClient()
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("leave request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("leave returned HTTP %d", resp.StatusCode)
	}
	return nil
}

// raftAddrForPod returns the Raft HTTP address for a given pod.
func raftAddrForPod(podName, svc, ns string) string {
	return fmt.Sprintf("http://%s.%s.%s.%s:%d", podName, svc, ns, clusterDomain, raftPort)
}
