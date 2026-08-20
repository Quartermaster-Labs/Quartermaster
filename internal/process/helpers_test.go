package process

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

var simpleResponderPath string

func skipIfNoSimpleResponder(t *testing.T) {
	t.Helper()
	if _, err := os.Stat(simpleResponderPath); os.IsNotExist(err) {
		t.Skipf("simple-responder not found at %s, run `make simple-responder`", simpleResponderPath)
	}
}

func TestMain(m *testing.M) {
	goos := runtime.GOOS
	goarch := runtime.GOARCH
	if goos == "windows" {
		simpleResponderPath = filepath.Join("..", "..", "build", "simple-responder.exe")
	} else {
		simpleResponderPath = filepath.Join("..", "..", "build", fmt.Sprintf("simple-responder_%s_%s", goos, goarch))
	}
	// Absolutise: the helper path is relative to this package dir, but processes
	// are spawned with cmd.Dir = spawnDir() (the running binary's directory —
	// under `go test` that is the temp build dir, not the package dir), and a
	// relative program path is resolved against Dir by exec. Left relative, every
	// test that actually starts simple-responder fails to exec it.
	if abs, err := filepath.Abs(simpleResponderPath); err == nil {
		simpleResponderPath = abs
	}
	m.Run()
}

func getFreePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("getFreePort: %v", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func simpleResponderCmd(t *testing.T, args ...string) (string, int) {
	port := getFreePort(t)
	cmdPath := filepath.ToSlash(simpleResponderPath)
	base := []string{cmdPath, fmt.Sprintf("-port %d", port)}
	base = append(base, args...)
	return strings.Join(base, " "), port
}
