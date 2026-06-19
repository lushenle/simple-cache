//go:build e2e
// +build e2e

package e2e

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"
)

const (
	clusterName = "simple-cache-e2e"
	operatorIMG = "ishenle/simple-cache-operator:e2e"
	cacheIMG    = "ishenle/simple-cache:e2e"
)

// TestE2E runs the end-to-end test using a kind cluster.
// Prerequisites: kind, kubectl, docker, and network access to Docker Hub.
// Run with: go test -v -count=1 -timeout 30m ./test/e2e/
func TestE2E(t *testing.T) {
	ctx := context.Background()

	// Build paths are relative to operator/ root, but go test runs in test/e2e/.
	// chdir to operator root so relative paths work for Dockerfile, kubectl manifests, etc.
	const root = "../.."
	if err := os.Chdir(root); err != nil {
		t.Fatalf("chdir to operator root: %v", err)
	}

	// --- Step 1: Create kind cluster ---
	t.Log("Creating kind cluster...")
	run(t, "kind", "create", "cluster", "--name", clusterName, "--image", "kindest/node:local", "--wait", "3m")
	defer func() {
		t.Log("Deleting kind cluster...")
		run(t, "kind", "delete", "cluster", "--name", clusterName)
	}()

	// --- Step 2: Build images and load into kind ---
	t.Log("Building operator image...")
	run(t, "docker", "build", "-t", operatorIMG, "-f", "Dockerfile", ".")
	run(t, "kind", "load", "docker-image", operatorIMG, "--name", clusterName)

	t.Log("Building cache image from repo root...")
	run(t, "docker", "build", "-t", cacheIMG, "-f", "../Dockerfile", "..")
	run(t, "kind", "load", "docker-image", cacheIMG, "--name", clusterName)

	// --- Step 3: Install CRD ---
	t.Log("Installing CRD...")
	run(t, "kubectl", "apply", "-f", "config/crd/bases/cache.shenle.lu_cacheclusters.yaml")
	run(t, "kubectl", "wait", "--for=condition=established", "--timeout=30s",
		"crd/cacheclusters.cache.shenle.lu")

	// --- Step 4: Deploy RBAC + operator ---
	t.Log("Creating system namespace...")
	createNS := exec.CommandContext(ctx, "kubectl", "create", "namespace", "system")
	createNS.Stdout, createNS.Stderr = os.Stdout, os.Stderr
	_ = createNS.Run() // ignore error if already exists

	t.Log("Deploying RBAC...")
	run(t, "kubectl", "apply", "-f", "config/rbac/service_account.yaml")
	run(t, "kubectl", "apply", "-f", "config/rbac/role.yaml")
	run(t, "kubectl", "apply", "-f", "config/rbac/role_binding.yaml")

	// Patch manager.yaml with e2e image
	tmpFile := "/tmp/manager-e2e.yaml"
	defer os.Remove(tmpFile)

	cmd := exec.CommandContext(ctx, "sed",
		"s|controller:latest|"+operatorIMG+"|g",
		"config/manager/manager.yaml")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("sed: %v", err)
	}
	if err := os.WriteFile(tmpFile, out, 0o644); err != nil {
		t.Fatalf("write tmp: %v", err)
	}

	t.Log("Deploying operator...")
	run(t, "kubectl", "apply", "-f", tmpFile)
	run(t, "kubectl", "wait", "--for=condition=ready", "--timeout=60s",
		"pod", "-l", "control-plane=controller-manager", "-n", "system")

	// --- Step 5: Create CacheCluster CR ---
	t.Log("Creating CacheCluster...")
	cacheCR := []byte(`
apiVersion: cache.shenle.lu/v1
kind: CacheCluster
metadata:
  name: e2e-test
spec:
  replicas: 3
  image: ishenle/simple-cache:e2e
  imagePullPolicy: Never
  cache:
    maxKeys: 1000
    evictionPolicy: lru
  raft:
    heartbeatMs: 200
    electionMs: 5000
    snapshotEnabled: true
    snapshotThreshold: 1024
  persistence:
    dumpOnShutdown: true
    loadOnStartup: false
    dumpFormat: binary
    dataDir: /data
  storage:
    size: 1Gi
`)

	crFile := "/tmp/cachecluster-e2e.yaml"
	defer os.Remove(crFile)
	if err := os.WriteFile(crFile, cacheCR, 0o644); err != nil {
		t.Fatalf("write cr: %v", err)
	}
	run(t, "kubectl", "apply", "-f", crFile)

	// --- Step 6: Verify resources ---
	time.Sleep(5 * time.Second) // let the controller reconcile

	t.Log("Verifying StatefulSet...")
	run(t, "kubectl", "get", "statefulset", "e2e-test", "-o", "yaml")

	t.Log("Verifying Services...")
	run(t, "kubectl", "get", "service", "e2e-test")
	run(t, "kubectl", "get", "service", "e2e-test-client")

	t.Log("Verifying ConfigMap...")
	run(t, "kubectl", "get", "configmap", "e2e-test")

	t.Log("Verifying CacheCluster status...")
	run(t, "kubectl", "get", "cachecluster", "e2e-test", "-o", "yaml")

	t.Log("E2E test completed!")
}

func run(t *testing.T, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("%s %v: %v", name, args, err)
	}
	time.Sleep(2 * time.Second)
}
