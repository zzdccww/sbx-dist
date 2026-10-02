//go:build tunnel_diagnostics

package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/sagernet/sing-box/log"
)

// Only diagnostic builds bypass the launcher's disabled logger. Sing-box
// traffic logging, the FFI contract and all reconnect decisions stay unchanged.
var (
	tunnelDiagnosticsOnce sync.Once
	tunnelDiagnosticsSink *tunnelLogSink
	proxyURI              = regexp.MustCompile(`(?i)(?:vmess|vless|hysteria2|hy2|trojan|ss)://[^\s"'<>]+`)
)

func newTunnelLogger(_ log.Factory, token string) log.ContextLogger {
	tunnelDiagnosticsOnce.Do(func() {
		path, err := filepath.Abs(filepath.Join(".sbx-diagnostics", "tunnel.log"))
		if err != nil {
			path = ""
		}
		tunnelDiagnosticsSink = &tunnelLogSink{path: path, console: os.Stderr, limit: 1 << 20}
	})
	return diagnosticTunnelLogger(tunnelDiagnosticsSink, token)
}

func recordTunnelProbe(healthy bool) {
	if tunnelDiagnosticsSink != nil {
		_, _ = fmt.Fprintf(tunnelDiagnosticsSink, "%s [sbx-diagnostic] edge_tcp_ok=%t (not a tunnel-health assertion)\n", time.Now().UTC().Format(time.RFC3339), healthy)
	}
}

func diagnosticTunnelLogger(sink io.Writer, token string) log.ContextLogger {
	w := &tunnelEventWriter{sink: sink, redactor: tunnelTokenRedactor(token)}
	factory := log.NewDefaultFactory(context.Background(), log.Formatter{
		DisableColors: true, FullTimestamp: true, TimestampFormat: time.RFC3339,
	}, w, "", nil, false)
	factory.SetLevel(log.LevelInfo)
	logger := factory.NewLogger("cloudflared")
	logger.Info("[sbx-diagnostic] tunnel-log-v1 enabled; file=.sbx-diagnostics/tunnel.log")
	return logger
}

// Mask the supplied token and decoded credential fields if an upstream error
// includes them. No sing-box logger uses this writer. Debug/trace stay disabled.
func tunnelTokenRedactor(token string) *strings.Replacer {
	var pairs []string
	add := func(secret string) {
		if len(secret) >= 8 {
			pairs = append(pairs, secret, "[redacted]")
		}
	}
	add(token)
	for _, encoding := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		decoded, err := encoding.DecodeString(strings.TrimSpace(token))
		if err != nil {
			continue
		}
		add(string(decoded))
		var fields map[string]json.RawMessage
		if json.Unmarshal(decoded, &fields) == nil {
			for _, field := range []string{"a", "t", "s"} {
				var value string
				if json.Unmarshal(fields[field], &value) == nil {
					add(value)
					if field == "s" {
						if secret, err := base64.StdEncoding.DecodeString(value); err == nil {
							add(string(secret))
						}
					}
				}
			}
		}
		break
	}
	return strings.NewReplacer(pairs...)
}

type tunnelEventWriter struct {
	sink     io.Writer
	redactor *strings.Replacer
}

func (w *tunnelEventWriter) Write(p []byte) (int, error) {
	text := string(p)
	// Keep lifecycle and origin errors, but omit routine destination traffic.
	if strings.Contains(text, ": inbound ") || strings.Contains(text, "registered V3 UDP session") {
		return len(p), nil
	}
	text = proxyURI.ReplaceAllString(w.redactor.Replace(text), "[redacted-node]")
	_, err := io.WriteString(w.sink, text)
	return len(p), err
}

// Files are closed after each write so Windows can rotate them too. Three
// bounded files live outside the launcher's normal .runtime cleanup directory.
type tunnelLogSink struct {
	mu      sync.Mutex
	path    string
	console io.Writer
	limit   int64
	warned  bool
}

func (w *tunnelLogSink) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := len(p)
	if int64(n) > w.limit {
		const suffix = " [truncated]\n"
		p = append(append([]byte(nil), p[:w.limit-int64(len(suffix))]...), suffix...)
	}
	_, _ = w.console.Write(p)
	if err := w.writeFile(p); err != nil && !w.warned {
		w.warned = true
		_, _ = fmt.Fprintln(w.console, "[sbx-diagnostic] file logging unavailable; Console logging continues")
	}
	// Disk/permission errors must not affect the running tunnel.
	return n, nil
}

func (w *tunnelLogSink) writeFile(p []byte) error {
	if w.path == "" {
		return fmt.Errorf("diagnostic path unavailable")
	}
	if err := os.MkdirAll(filepath.Dir(w.path), 0700); err != nil {
		return err
	}
	info, err := os.Stat(w.path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if info != nil && info.Size()+int64(len(p)) > w.limit {
		if err := os.Remove(w.path + ".2"); err != nil && !os.IsNotExist(err) {
			return err
		}
		if err := os.Rename(w.path+".1", w.path+".2"); err != nil && !os.IsNotExist(err) {
			return err
		}
		if err := os.Rename(w.path, w.path+".1"); err != nil {
			return err
		}
	}
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(p)
	closeErr := f.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}
