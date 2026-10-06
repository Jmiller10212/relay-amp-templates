//go:build linux

package integration

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
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

type lockedBuffer struct {
	sync.Mutex
	bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.Lock()
	defer b.Unlock()
	return b.Buffer.Write(p)
}
func (b *lockedBuffer) String() string { b.Lock(); defer b.Unlock(); return b.Buffer.String() }

type process struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	output  *lockedBuffer
	baseURL string
}

func TestBuiltBinaryHealthConsoleAndSignals(t *testing.T) {
	binary := os.Getenv("RELAY_BINARY")
	if binary == "" {
		t.Skip("RELAY_BINARY not set")
	}
	p := start(t, binary, t.TempDir())
	fmt.Fprintln(p.stdin, "help")
	fmt.Fprintln(p.stdin, "status")
	fmt.Fprintln(p.stdin, "users")
	fmt.Fprintln(p.stdin, "events")
	waitOutput(t, p, "COMMANDS ")
	waitOutput(t, p, "STATUS ready=true")
	waitOutput(t, p, "USERS none")
	fmt.Fprintln(p.stdin, "stop")
	waitExit(t, p)
	p = start(t, binary, t.TempDir())
	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	waitExit(t, p)
	waitOutput(t, p, "shutdown complete")
	p = start(t, binary, t.TempDir())
	if err := p.cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	waitExit(t, p)
}
func start(t *testing.T, binary, dataDir string) *process {
	t.Helper()
	port := freePort(t)
	cmd := exec.Command(binary, "--data-dir", dataDir, "--listen", "127.0.0.1", "--port", strconv.Itoa(port))
	cmd.Env = append(os.Environ(), "RELAY_SUPABASE_URL=http://127.0.0.1:1", "RELAY_SUPABASE_PUBLISHABLE_KEY=test-publishable")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out := &lockedBuffer{}
	cmd.Stdout = out
	cmd.Stderr = out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	p := &process{cmd: cmd, stdin: stdin, output: out, baseURL: fmt.Sprintf("http://127.0.0.1:%d", port)}
	waitOutput(t, p, "RELAY READY")
	res, err := http.Get(p.baseURL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("health status=%d body=%s", res.StatusCode, body)
	}
	var health map[string]any
	if err := json.NewDecoder(res.Body).Decode(&health); err != nil {
		t.Fatal(err)
	}
	return p
}
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}
func waitOutput(t *testing.T, p *process, want string) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(p.output.String(), want) {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("output never contained %q:\n%s", want, p.output.String())
}
func waitExit(t *testing.T, p *process) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- p.cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("process exit: %v\n%s", err, p.output.String())
		}
	case <-time.After(10 * time.Second):
		_ = p.cmd.Process.Kill()
		t.Fatalf("process did not exit\n%s", p.output.String())
	}
}

var _ = context.Background
