package process

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/quartermaster-labs/quartermaster/internal/config"
)

// A backend that dies during startup gets its own error line into the start
// error, which is what the client's 500 body and the proxy log carry.
func TestProcessCommand_PrematureExitNamesBackendError(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	const msg = `error while handling argument "--chat-template-file": error: failed to open file 'D:/missing.jinja'`
	port := getFreePort(t)
	p := newProcessCommand(t, config.ModelConfig{
		Cmd:                `"` + filepath.ToSlash(exe) + `"`,
		Env:                []string{exitWithEnv + "=" + msg},
		Proxy:              fmt.Sprintf("http://127.0.0.1:%d", port),
		CheckEndpoint:      "/health",
		HealthCheckTimeout: 10,
	})
	t.Cleanup(func() { p.Stop(testStopTimeout) })

	err = p.Run(testStartTimeout)
	if err == nil {
		t.Fatal("Run succeeded, want a premature-exit error")
	}
	if !strings.Contains(err.Error(), "exited prematurely: "+msg) {
		t.Fatalf("Run error = %q, want it to carry the backend's line", err)
	}
}

func TestExitTail_ArgumentError(t *testing.T) {
	var tail exitTail
	tail.Write([]byte("load_backend: loaded CUDA backend\r\n"))
	tail.Write([]byte(`error while handling argument "--chat-template-file": error: failed to open file 'D:/x/chat_template.jinja'` + "\r\n"))

	got := tail.reason()
	want := `error while handling argument "--chat-template-file": error: failed to open file 'D:/x/chat_template.jinja'`
	if got != want {
		t.Fatalf("reason = %q, want %q", got, want)
	}
}

func TestExitTail_CascadePicksRootCause(t *testing.T) {
	var tail exitTail
	tail.Write([]byte(strings.Join([]string{
		"print_info: n_ctx_train = 262144",
		"ggml_backend_cuda_buffer_type_alloc_buffer: allocating 18000.00 MiB on device 0: cudaMalloc failed: out of memory",
		"llama_model_load: error loading model: unable to allocate CUDA0 buffer",
		"srv    load_model: failed to load model",
		"main: exiting due to model loading error",
	}, "\n")))

	if got := tail.reason(); !strings.Contains(got, "cudaMalloc failed: out of memory") {
		t.Fatalf("reason = %q, want the cudaMalloc line", got)
	}
}

func TestExitTail_NoErrorLineFallsBackToLast(t *testing.T) {
	var tail exitTail
	tail.Write([]byte("starting\nsomething odd happened\n\n"))
	if got := tail.reason(); got != "something odd happened" {
		t.Fatalf("reason = %q", got)
	}
}

func TestExitTail_Empty(t *testing.T) {
	var tail exitTail
	if got := tail.reason(); got != "" {
		t.Fatalf("reason = %q, want empty", got)
	}
}

func TestExitTail_BoundedAndDropsCutLine(t *testing.T) {
	var tail exitTail
	// An early error must age out once the buffer wraps past it.
	tail.Write([]byte("early error that scrolled away\n"))
	tail.Write([]byte(strings.Repeat("x", exitTailMax) + "\nfinal error: boom\n"))

	if len(tail.buf) > exitTailMax {
		t.Fatalf("buf len %d exceeds cap %d", len(tail.buf), exitTailMax)
	}
	if got := tail.reason(); got != "final error: boom" {
		t.Fatalf("reason = %q, want the final line", got)
	}
}

func TestExitTail_ClipsLongLine(t *testing.T) {
	var tail exitTail
	tail.Write([]byte("error: " + strings.Repeat("é", 1000)))
	got := tail.reason()
	if n := len([]rune(got)); n != exitReasonMax+3 {
		t.Fatalf("clipped rune count = %d, want %d", n, exitReasonMax+3)
	}
}
