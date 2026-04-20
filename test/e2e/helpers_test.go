//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"

	"github.com/lushenle/simple-cache/pkg/client"
	"github.com/lushenle/simple-cache/pkg/common"
	"github.com/lushenle/simple-cache/pkg/config"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

const (
	singleWaitTimeout      = 10 * time.Second
	distributedWaitTimeout = 15 * time.Second
	pollInterval           = 200 * time.Millisecond
	processStopTimeout     = 8 * time.Second
)

type probeResponse struct {
	Status   common.ProbeState `json:"status"`
	Mode     common.Mode       `json:"mode"`
	Ready    bool              `json:"ready"`
	Role     string            `json:"role"`
	LeaderID string            `json:"leader_id,omitempty"`
	Details  map[string]any    `json:"details,omitempty"`
}

type nodeConfig struct {
	name string
	cfg  *config.Config
}

type nodeProcess struct {
	name       string
	cfg        *config.Config
	configPath string
	cmd        *exec.Cmd
	stdout     bytes.Buffer
	stderr     bytes.Buffer
	exitCh     chan error
}

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := l.Addr().String()
	require.NoError(t, l.Close())
	return addr
}

func writeConfig(t *testing.T, cfg *config.Config, dir, name string) string {
	t.Helper()
	data, err := yaml.Marshal(cfg)
	require.NoError(t, err)

	path := filepath.Join(dir, fmt.Sprintf("%s.yaml", name))
	require.NoError(t, os.WriteFile(path, data, 0o644))
	return path
}

func startNodeProcess(t *testing.T, root string, spec nodeConfig) *nodeProcess {
	t.Helper()

	configPath := writeConfig(t, spec.cfg, t.TempDir(), spec.name)
	cmd := exec.Command("go", "run", "./pkg/cmd/main.go")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "CONFIG_PATH="+configPath)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	proc := &nodeProcess{
		name:       spec.name,
		cfg:        spec.cfg,
		configPath: configPath,
		cmd:        cmd,
		exitCh:     make(chan error, 1),
	}
	cmd.Stdout = &proc.stdout
	cmd.Stderr = &proc.stderr

	require.NoError(t, cmd.Start(), "failed to start %s", spec.name)
	go func() {
		proc.exitCh <- cmd.Wait()
	}()

	t.Cleanup(func() {
		proc.stop(t)
	})

	return proc
}

func (n *nodeProcess) stop(t *testing.T) {
	t.Helper()
	if n == nil || n.cmd == nil || n.cmd.Process == nil {
		return
	}
	if n.cmd.ProcessState != nil && n.cmd.ProcessState.Exited() {
		return
	}

	select {
	case <-n.exitCh:
		return
	default:
	}

	_ = syscall.Kill(-n.cmd.Process.Pid, syscall.SIGTERM)

	select {
	case <-time.After(processStopTimeout):
		_ = syscall.Kill(-n.cmd.Process.Pid, syscall.SIGKILL)
		select {
		case <-n.exitCh:
		case <-time.After(3 * time.Second):
		}
	case <-n.exitCh:
	}
}

func (n *nodeProcess) dumpLogs() string {
	return fmt.Sprintf("node=%s\nconfig=%s\nstdout:\n%s\nstderr:\n%s\n",
		n.name,
		n.configPath,
		n.stdout.String(),
		n.stderr.String(),
	)
}

func newE2EClient(t *testing.T, addr string) *client.Client {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cli, err := client.New(ctx, addr)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = cli.Close()
	})
	return cli
}

func httpGetJSON(t *testing.T, url string, expectedStatus int, out any) {
	t.Helper()
	resp, err := http.Get(url) //nolint:noctx
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, expectedStatus, resp.StatusCode, "url=%s body=%s", url, string(body))
	if out != nil {
		require.NoError(t, json.Unmarshal(body, out), "url=%s body=%s", url, string(body))
	}
}

func httpPostJSON(t *testing.T, url string, expectedStatus int, payload any, out any) {
	t.Helper()
	data, err := json.Marshal(payload)
	require.NoError(t, err)
	resp, err := http.Post(url, "application/json", bytes.NewReader(data)) //nolint:noctx
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, expectedStatus, resp.StatusCode, "url=%s body=%s", url, string(body))
	if out != nil {
		require.NoError(t, json.Unmarshal(body, out), "url=%s body=%s", url, string(body))
	}
}

func waitHTTPReady(t *testing.T, node *nodeProcess, path string, expectedStatus int, timeout time.Duration) probeResponse {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastStatus int
	var lastBody []byte

	url := fmt.Sprintf("http://%s%s", node.cfg.HTTPAddr, path)
	for time.Now().Before(deadline) {
		select {
		case err := <-node.exitCh:
			require.FailNowf(t, "node exited before ready", "node=%s err=%v\n%s", node.name, err, node.dumpLogs())
		default:
		}

		resp, err := http.Get(url) //nolint:noctx
		if err == nil {
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			lastStatus = resp.StatusCode
			lastBody = body
			if resp.StatusCode == expectedStatus {
				var out probeResponse
				require.NoError(t, json.Unmarshal(body, &out), "node=%s body=%s", node.name, string(body))
				return out
			}
		}
		time.Sleep(pollInterval)
	}

	require.FailNowf(t, "timeout waiting endpoint", "node=%s url=%s expected=%d lastStatus=%d lastBody=%s\n%s",
		node.name, url, expectedStatus, lastStatus, string(lastBody), node.dumpLogs())
	return probeResponse{}
}

func waitForReplication(t *testing.T, timeout time.Duration, fn func() bool, dumpers ...*nodeProcess) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(pollInterval)
	}

	var details bytes.Buffer
	for _, node := range dumpers {
		if node != nil {
			details.WriteString(node.dumpLogs())
		}
	}
	require.FailNowf(t, "replication timeout", "%s", details.String())
}

func waitLeader(t *testing.T, nodes []*nodeProcess, timeout time.Duration) *nodeProcess {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var leader *nodeProcess
		readyCount := 0
		for _, node := range nodes {
			resp, statusCode, ok := tryProbe(node, "/readyz")
			if !ok {
				continue
			}
			if statusCode == http.StatusOK && resp.Ready {
				leader = node
				readyCount++
			}
		}
		if readyCount == 1 {
			return leader
		}
		time.Sleep(pollInterval)
	}

	var details bytes.Buffer
	for _, node := range nodes {
		details.WriteString(node.dumpLogs())
	}
	require.FailNowf(t, "leader election timeout", "%s", details.String())
	return nil
}

func tryProbe(node *nodeProcess, path string) (probeResponse, int, bool) {
	url := fmt.Sprintf("http://%s%s", node.cfg.HTTPAddr, path)
	resp, err := http.Get(url) //nolint:noctx
	if err != nil {
		return probeResponse{}, 0, false
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return probeResponse{}, resp.StatusCode, false
	}
	var out probeResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return probeResponse{}, resp.StatusCode, false
	}
	return out, resp.StatusCode, true
}
