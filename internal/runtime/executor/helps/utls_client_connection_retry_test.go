package helps

import (
	"context"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"

	requestlog "github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

// Each subprocess gets a fresh system trust pool for its local TLS fixture.
func TestUtlsConnectionSetupRetry(t *testing.T) {
	mode := os.Getenv("CPA_SETUP_RETRY_CHILD")
	if mode == "" {
		executable, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		for _, scenario := range []string{"dial", "handshake", "dial_h2", "handshake_h2", "persistent", "certificate", "cancel", "after_http", "after_headers", "permanent"} {
			t.Run(scenario, func(t *testing.T) {
				command := exec.Command(executable, "-test.run=^TestUtlsConnectionSetupRetry$", "-test.v")
				command.Env = append(os.Environ(), "CPA_SETUP_RETRY_CHILD="+scenario)
				if output, errRun := command.CombinedOutput(); errRun != nil {
					t.Fatalf("child failed: %v\n%s", errRun, output)
				}
			})
		}
		return
	}
	logger := log.StandardLogger()
	previousHooks := logger.ReplaceHooks(make(log.LevelHooks))
	hook := new(logtest.Hook)
	logger.AddHook(hook)
	t.Cleanup(func() { logger.ReplaceHooks(previousHooks) })
	var requests atomic.Int32
	const payload = "synthetic-body-once"
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		if string(body) != payload {
			t.Errorf("body = %q", body)
		}
		requests.Add(1)
		if mode == "after_headers" {
			w.Header().Set("Content-Length", "100")
			_, _ = w.Write([]byte("partial"))
			w.(http.Flusher).Flush()
			return
		}
		if mode == "after_http" {
			connection, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			_ = connection.Close()
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	server.EnableHTTP2 = strings.HasSuffix(mode, "_h2")
	server.StartTLS()
	defer server.Close()
	if mode != "certificate" {
		cert := filepath.Join(t.TempDir(), "root.pem")
		if err := os.WriteFile(cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("SSL_CERT_FILE", cert)
		t.Setenv("SSL_CERT_DIR", t.TempDir())
	}
	const correlationID = "00000000-0000-7000-8000-000000000001"
	ctx, cancel := context.WithCancel(requestlog.WithRequestID(context.Background(), correlationID))
	defer cancel()
	calls := 0
	var broken *trackedNetConn
	dialer := contextDialerFunc(func(ctx context.Context, network, address string) (net.Conn, error) {
		calls++
		if mode == "cancel" {
			cancel()
			return nil, &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNRESET}
		}
		if mode == "permanent" {
			return nil, errors.New("unsupported proxy configuration")
		}
		if mode == "persistent" || (strings.HasPrefix(mode, "dial") && calls == 1) {
			return nil, &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}
		}
		if strings.HasPrefix(mode, "handshake") && calls == 1 {
			connection, peer := net.Pipe()
			_ = peer.Close()
			broken = &trackedNetConn{Conn: connection}
			return broken, nil
		}
		return (&net.Dialer{}).DialContext(ctx, network, address)
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL, strings.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	transport := &utlsRoundTripper{dialer: dialer}
	response, errRoundTrip := transport.RoundTrip(req)
	if response != nil {
		_, errRead := io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
		if mode == "after_headers" && !errors.Is(errRead, io.ErrUnexpectedEOF) {
			t.Fatalf("expected truncated HTTP 200 body, got %v", errRead)
		}
		if strings.HasSuffix(mode, "_h2") && response.ProtoMajor != 2 {
			t.Fatalf("expected real HTTP/2, got %s", response.Proto)
		}
	}
	wantCalls, wantRequests := 1, 0
	switch mode {
	case "dial", "handshake", "dial_h2", "handshake_h2":
		wantCalls, wantRequests = 2, 1
		if errRoundTrip != nil {
			t.Errorf("connection recovery failed: %v", errRoundTrip)
		}
	case "persistent":
		wantCalls = 2
	case "after_http", "after_headers":
		wantRequests = 1
	}
	stages := map[string]int{}
	for _, entry := range hook.AllEntries() {
		if !strings.HasPrefix(entry.Message, "cpa_connection_stage ") {
			continue
		}
		if entry.Data["correlation_id"] != correlationID {
			t.Error("stage lost request correlation")
		}
		formatted, errFormat := (&requestlog.LogFormatter{}).Format(entry)
		if errFormat != nil || !strings.Contains(string(formatted), "correlation_id=\""+correlationID+"\"") {
			t.Error("production formatter lost stage correlation")
		}
		stage, _ := entry.Data["stage"].(string)
		stages[stage]++
		if milliseconds, ok := entry.Data["duration_ms"].(float64); !ok || milliseconds < 0 {
			t.Error("invalid stage duration")
		}
	}
	if stages["dial"] != calls {
		t.Errorf("dial stage events=%d, attempts=%d", stages["dial"], calls)
	}
	if wantRequests > 0 && stages["response_headers"] != 1 {
		t.Errorf("HTTP stage events=%v", stages)
	}
	if calls != wantCalls {
		t.Errorf("dial calls = %d, want %d", calls, wantCalls)
	}
	if int(requests.Load()) != wantRequests {
		t.Errorf("HTTP requests = %d, want %d", requests.Load(), wantRequests)
	}
	if wantRequests == 0 && errRoundTrip == nil {
		t.Fatal("expected failure")
	}
	if mode == "after_http" && errRoundTrip == nil {
		t.Fatal("expected response EOF without replay")
	}
	if strings.HasPrefix(mode, "handshake") && broken.closeCount.Load() == 0 {
		t.Fatal("failed handshake connection leaked")
	}
}
