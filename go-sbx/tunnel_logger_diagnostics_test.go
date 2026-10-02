//go:build tunnel_diagnostics

package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestTunnelDiagnosticsKeepsRecoveryEventsAndRedactsCredentials(t *testing.T) {
	secret := "tunnel-secret-sentinel"
	encodedSecret := base64.StdEncoding.EncodeToString([]byte(secret))
	decoded := fmt.Sprintf(`{"a":"account-sentinel","t":"tunnel-id-sentinel","s":%q}`, encodedSecret)
	token := base64.StdEncoding.EncodeToString([]byte(decoded))
	var output bytes.Buffer
	logger := diagnosticTunnelLogger(&output, token)
	logger.Info("connected to edge")
	logger.WarnContext(context.Background(), "connection 1 switching to fallback protocol http2")
	logger.Error("connection 1 failed permanently: registration refused")
	logger.Error("connection 2 failed, retrying in 2s; ", token, " ", decoded, " ", encodedSecret, " ", secret)
	logger.ErrorContext(context.Background(), "origin request: vmess://NODE_SENTINEL vless://OTHER_NODE")
	logger.InfoContext(context.Background(), "inbound TCP connection to TRAFFIC_SENTINEL")
	logger.Debug("DEBUG_SENTINEL")
	text := output.String()
	for _, want := range []string{"tunnel-log-v1", "connected to edge", "fallback protocol http2", "failed permanently", "retrying in 2s", "[redacted]", "[redacted-node]"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing diagnostic event %q", want)
		}
	}
	for _, forbidden := range []string{token, secret, encodedSecret, "account-sentinel", "tunnel-id-sentinel", "NODE_SENTINEL", "OTHER_NODE", "TRAFFIC_SENTINEL", "DEBUG_SENTINEL"} {
		if strings.Contains(text, forbidden) {
			t.Errorf("diagnostic output leaked filtered data")
		}
	}
}

func TestTunnelDiagnosticFilesRotateAndKeepNewestEvents(t *testing.T) {
	dir := t.TempDir()
	var console bytes.Buffer
	sink := &tunnelLogSink{path: filepath.Join(dir, "logs", "tunnel.log"), console: &console, limit: 128}
	for i := 0; i < 30; i++ {
		line := fmt.Sprintf("event-%02d %s\n", i, strings.Repeat("x", 40))
		if n, err := sink.Write([]byte(line)); err != nil || n != len(line) {
			t.Fatalf("write failed: n=%d err=%v", n, err)
		}
	}
	files, err := os.ReadDir(filepath.Dir(sink.path))
	if err != nil || len(files) != 3 {
		t.Fatalf("expected exactly three log files: files=%v err=%v", files, err)
	}
	for _, file := range files {
		info, err := file.Info()
		if err != nil || info.Size() > sink.limit {
			t.Fatalf("log size exceeded limit: %v", err)
		}
	}
	current, err := os.ReadFile(sink.path)
	if err != nil || !bytes.Contains(current, []byte("event-29")) {
		t.Fatalf("latest event missing: %v", err)
	}
	large := []byte(strings.Repeat("z", 300))
	if n, err := sink.Write(large); err != nil || n != len(large) {
		t.Fatalf("oversized event changed Write contract: n=%d err=%v", n, err)
	}
	current, _ = os.ReadFile(sink.path)
	if int64(len(current)) > sink.limit || !bytes.Contains(current, []byte("[truncated]")) {
		t.Fatal("oversized event was not bounded")
	}
}

func TestTunnelDiagnosticDiskFailureDoesNotInterruptConsole(t *testing.T) {
	parentFile := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(parentFile, []byte("occupied"), 0600); err != nil {
		t.Fatal(err)
	}
	var console bytes.Buffer
	sink := &tunnelLogSink{path: filepath.Join(parentFile, "tunnel.log"), console: &console, limit: 128}
	for _, line := range []string{"first event\n", "second event\n"} {
		if n, err := sink.Write([]byte(line)); err != nil || n != len(line) {
			t.Fatalf("disk failure propagated: %v", err)
		}
	}
	if strings.Count(console.String(), "file logging unavailable") != 1 || !strings.Contains(console.String(), "second event") {
		t.Fatal("console fallback or warning deduplication failed")
	}
}

func TestTunnelDiagnosticConcurrentConnectionLogs(t *testing.T) {
	var console bytes.Buffer
	sink := &tunnelLogSink{path: filepath.Join(t.TempDir(), "tunnel.log"), console: &console, limit: 1 << 20}
	logger := diagnosticTunnelLogger(sink, "token-sentinel")
	var done sync.WaitGroup
	for i := 0; i < 8; i++ {
		done.Add(1)
		go func(index int) {
			defer done.Done()
			for n := 0; n < 20; n++ {
				logger.Error("connection ", index, " failed, retrying")
			}
		}(i)
	}
	done.Wait()
	file, err := os.ReadFile(sink.path)
	if err != nil || strings.Count(string(file), "failed, retrying") != 160 {
		t.Fatalf("concurrent events lost: %v", err)
	}
	if string(file) != console.String() {
		t.Fatal("file and Console events differ")
	}
}
