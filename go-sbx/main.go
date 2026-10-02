package main

import "C"
import (
	"context"
	"fmt"
	"net"
	"os"
	"sync"
	"time"

	box "github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	cloudflared "github.com/sagernet/sing-cloudflared"
	N "github.com/sagernet/sing/common/network"
)

// Startup has three states rather than a single `running` bool because the
// network-bound part of StartSingBox runs *without* holding mu — see the
// comment in StartSingBox.
const (
	stateIdle = iota
	stateStarting
	stateRunning
)

var (
	mu        sync.Mutex
	state     int
	boxInst   *box.Box
	tunnelSvc *cloudflared.Service
	cancelFn  context.CancelFunc
	// abortStart is set when StopSingBox arrives mid-startup; the starting
	// goroutine owns the rollback so Start and Close never run concurrently.
	abortStart bool
	// startDone is closed by the starter once the slow phase has settled,
	// letting StopSingBox wait for a real teardown instead of returning early.
	startDone chan struct{}

	// === Tunnel watchdog state ===
	watchdogCtx    context.Context
	watchdogCancel context.CancelFunc
	watchdogDone   chan struct{}
	reconnecting   bool // mu protects: prevents re-entry during reconnect

	// === Saved Tunnel payload for reconnection ===
	lastTunnelPayload struct {
		Token    string
		Hostname string
	}
)

//export StartSingBox
func StartSingBox(payloadJSON *C.char) C.int {
	// --- Phase 1: cheap, non-blocking work, under the lock. ---
	mu.Lock()
	if state != stateIdle {
		mu.Unlock()
		fmt.Fprintln(os.Stderr, "[sbx] already running")
		return 1
	}

	// C.GoString copies into Go memory, so payloadJSON does not need to
	// outlive this call — the caller may free it as soon as we return.
	payload, err := parsePayload(C.GoString(payloadJSON))
	if err != nil {
		mu.Unlock()
		fmt.Fprintf(os.Stderr, "[sbx] payload error: %v\n", err)
		return 2
	}

	if err := os.Chdir(payload.WorkingDir); err != nil {
		mu.Unlock()
		fmt.Fprintf(os.Stderr, "[sbx] chdir error: %v\n", err)
		return 2
	}

	configData, err := os.ReadFile(payload.Config)
	if err != nil {
		mu.Unlock()
		fmt.Fprintf(os.Stderr, "[sbx] config read error: %v\n", err)
		return 3
	}

	ctx, cancel := context.WithCancel(context.Background())
	ctx = registryContext(ctx)

	var options option.Options
	if err := options.UnmarshalJSONContext(ctx, configData); err != nil {
		mu.Unlock()
		cancel()
		fmt.Fprintf(os.Stderr, "[sbx] config parse error: %v\n", err)
		return 3
	}

	if payload.DisableColor {
		if options.Log == nil {
			options.Log = &option.LogOptions{}
		}
		options.Log.DisableColor = true
	}

	// Claim the slot, then release the lock for the slow phase below.
	// box.Start() synchronously downloads every remote rule_set with no
	// client-side timeout (route/router.go StartStateStart), and the real
	// launcher configs carry 2-3 of them whenever WARP_MODE != off. Holding
	// mu across that would make StopSingBox block on mu.Lock() for as long
	// as the download stalls, hanging the host's shutdown hook.
	state = stateStarting
	abortStart = false
	done := make(chan struct{})
	startDone = done
	// Publish cancel now, not at phase 3: StopSingBox needs it to interrupt a
	// stalled rule-set download rather than wait for it to finish.
	cancelFn = cancel
	mu.Unlock()

	// rollback tears down whatever was built and returns the slot to idle.
	rollback := func(inst *box.Box, svc *cloudflared.Service) {
		if svc != nil {
			svc.Close()
		}
		if inst != nil {
			inst.Close()
		}
		cancel()
		mu.Lock()
		state = stateIdle
		boxInst, tunnelSvc, cancelFn, startDone = nil, nil, nil, nil
		mu.Unlock()
		close(done)
	}

	// --- Phase 2: slow, network-bound work, lock released. ---
	instance, err := box.New(box.Options{Context: ctx, Options: options})
	if err != nil {
		rollback(nil, nil)
		fmt.Fprintf(os.Stderr, "[sbx] box create error: %v\n", err)
		return 4
	}

	if err := instance.Start(); err != nil {
		// box.Start already closed the instance internally on failure.
		rollback(nil, nil)
		fmt.Fprintf(os.Stderr, "[sbx] box start error: %v\n", err)
		return 4
	}

	// A StopSingBox that arrived while we were starting wins: unwind rather
	// than publish an instance nobody is going to stop.
	mu.Lock()
	aborted := abortStart
	mu.Unlock()
	if aborted {
		rollback(instance, nil)
		fmt.Fprintln(os.Stderr, "[sbx] start aborted by StopSingBox")
		return 4
	}

	// Startup is atomic: if the tunnel fails, roll the sing-box instance back
	// so the port is released and a later retry starts from a clean state.
	var svc *cloudflared.Service
	if payload.Tunnel != nil && payload.Tunnel.Token != "" {
		// ConnectionDialer MUST be set. sing-cloudflared defaults ControlDialer
		// and TunnelDialer to N.SystemDialer when nil (service.go:155-161) but
		// assigns ConnectionDialer straight through (service.go:183). It is the
		// dialer used to reach the local origin, so leaving it nil panics with a
		// nil dereference in dialRouterTCPWithMetadata (router_pipe.go:23) on the
		// first request the tunnel delivers — taking the whole host process down.
		//
		// NewService and Start both succeed without it; only real traffic trips
		// it. That is why smoke_test case 8, which stops at token parsing, never
		// caught this.
		//
		// Route tunnel logs through sing-box's factory too. Without an explicit
		// logger sing-cloudflared falls back to logger.NOP() and silently
		// discards everything — leaving no trace when the tunnel fails to
		// reach the Cloudflare edge.
		svc, err = cloudflared.NewService(cloudflared.ServiceOptions{
			Token:            payload.Tunnel.Token,
			Logger:           newTunnelLogger(instance.LogFactory(), payload.Tunnel.Token),
			ConnectionDialer: N.SystemDialer,
			// GracePeriod caps how long gracefulShutdown waits on StopSingBox.
			// sing-cloudflared defaults to 30s (service.go:145-148), and
			// gracefulShutdown runs under serveCtx = context.WithoutCancel(ctx)
			// (connection_quic.go:187) — so cancelling the service ctx cannot
			// preempt it; the only lever is this timer. 30s makes StopSingBox
			// block ~30s on a live tunnel, which the launchers' SIGTERM window
			// cannot survive (they SIGKILL first). Cap it at 5s so a live
			// tunnel tears down in well under the launcher's stop timeout.
			GracePeriod: 5 * time.Second,
		})
		if err != nil {
			rollback(instance, nil)
			fmt.Fprintf(os.Stderr, "[sbx] tunnel create error: %v\n", err)
			return 5
		}
		if err := svc.Start(); err != nil {
			rollback(instance, svc)
			fmt.Fprintf(os.Stderr, "[sbx] tunnel start error: %v\n", err)
			return 5
		}

		// Save tunnel payload for reconnection
		lastTunnelPayload.Token = payload.Tunnel.Token
		lastTunnelPayload.Hostname = payload.Tunnel.Hostname
	}

	// --- Phase 3: publish. ---
	mu.Lock()
	aborted = abortStart
	if !aborted {
		boxInst = instance
		cancelFn = cancel
		tunnelSvc = svc
		state = stateRunning
		startDone = nil

		// Start watchdog if tunnel is enabled
		if svc != nil {
			watchdogDone = make(chan struct{})
			watchdogCtx, watchdogCancel = context.WithCancel(context.Background())
			go startTunnelWatchdog(watchdogCtx, instance.LogFactory())
		}
	}
	mu.Unlock()

	if aborted {
		rollback(instance, svc)
		fmt.Fprintln(os.Stderr, "[sbx] start aborted by StopSingBox")
		return 4
	}

	close(done)
	fmt.Fprintln(os.Stderr, "[sbx] started successfully")
	return 0
}

// StopSingBox returns int, not void: all three launchers declare it that way
// (JNA invokeInt, koffi `int StopSingBox()`, ctypes restype=c_int) and print
// the result as a status code. Exporting void left them reading a garbage
// register and reporting it as "stopped with code <junk>".
//
// 0 = stopped cleanly (or was already idle), 6 = stopped but a Close() failed.
//
//export StopSingBox
func StopSingBox() C.int {
	mu.Lock()

	switch state {
	case stateIdle:
		mu.Unlock()
		return 0

	case stateStarting:
		// Startup is mid-flight and holds no lock. Flag the abort and cancel
		// the context — that unblocks any in-progress rule-set download — then
		// wait outside the lock for the starter to run its own rollback, so
		// Close never races with Start.
		abortStart = true
		cancel, done := cancelFn, startDone
		mu.Unlock()
		if cancel != nil {
			cancel()
		}
		if done != nil {
			<-done
		}
		fmt.Fprintln(os.Stderr, "[sbx] stopped (start aborted)")
		return 0
	}

	// stateRunning: notify watchdog to exit first
	if watchdogCancel != nil {
		watchdogCancel()
	}
	done := watchdogDone
	mu.Unlock()

	// Wait for watchdog to exit completely (lock-free blocking)
	if done != nil {
		<-done
	}

	// Now take ownership of instances and close them
	mu.Lock()
	svc, inst, cancel := tunnelSvc, boxInst, cancelFn
	tunnelSvc, boxInst, cancelFn = nil, nil, nil
	state = stateIdle
	mu.Unlock()

	// Shutdown order per design: tunnel first, then sing-box, then context.
	// Both Close errors are reported rather than swallowed, but neither aborts
	// the sequence — the context must be cancelled either way.
	//
	// svc.Close() is the one call that can hang: sing-cloudflared's Close waits
	// on s.done.Wait() for every HA superviseConnection goroutine, and those
	// run gracefulShutdown under WithoutCancel(s.ctx) — ctx cancel cannot
	// preempt them, only GracePeriod can, and we capped that at 5s above. So in
	// the happy path svc.Close returns in ~5s. The belt-and-suspenders timeout
	// below (8s = 5s grace + 3s slack for the Unregister RPC / edge latency)
	// guarantees StopSingBox returns even if a future cloudflared change or a
	// pathological edge makes Close exceed GracePeriod. On timeout we stop
	// waiting: the tunnel goroutines keep draining on their own GracePeriod
	// timers (WithoutCancel ctx) and close the QUIC conn + packetConn when the
	// timer fires — no goroutine leak, no fd leak beyond the grace window, no
	// panic (closeOnce / shutdownOnce / connectionAccess mutex + recover all
	// guard the path; box.Close re-calling service.Close is idempotent).
	var failed bool
	if svc != nil {
		closeDone := make(chan error, 1)
		go func() { closeDone <- svc.Close() }()
		select {
		case err := <-closeDone:
			if err != nil {
				failed = true
				fmt.Fprintf(os.Stderr, "[sbx] tunnel close error: %v\n", err)
			}
		case <-time.After(8 * time.Second):
			failed = true
			fmt.Fprintln(os.Stderr, "[sbx] tunnel close timed out after 8s, continuing")
		}
	}
	if inst != nil {
		if err := inst.Close(); err != nil {
			failed = true
			fmt.Fprintf(os.Stderr, "[sbx] box close error: %v\n", err)
		}
	}
	if cancel != nil {
		cancel()
	}

	if failed {
		fmt.Fprintln(os.Stderr, "[sbx] stopped with errors")
		return 6
	}
	fmt.Fprintln(os.Stderr, "[sbx] stopped")
	return 0
}

// probeTunnelHealth 探测 Cloudflare Tunnel edge 可达性
// TCP 连接成功 = 网络层健康，QUIC 握手由 sing-cloudflared 处理
func probeTunnelHealth() bool {
	conn, err := net.DialTimeout("tcp", "region1.v2.argotunnel.com:7844", 5*time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// attemptReconnect 尝试重建 Tunnel Service
// 返回 true = 重建成功; false = 失败（需退避重试）
func attemptReconnect(ctx context.Context, logFactory log.Factory) bool {
	// 防重入检查
	mu.Lock()
	if reconnecting {
		mu.Unlock()
		return false
	}
	reconnecting = true
	mu.Unlock()

	defer func() {
		mu.Lock()
		reconnecting = false
		mu.Unlock()
	}()

	fmt.Fprintf(os.Stderr, "[sbx-watchdog] tunnel unhealthy, reconnecting...\n")

	// 快速退出检查
	select {
	case <-ctx.Done():
		return false
	default:
	}

	// 保存旧 Service，置 nil 防止 StopSingBox 重复 Close
	mu.Lock()
	oldSvc := tunnelSvc
	tunnelSvc = nil
	token := lastTunnelPayload.Token
	mu.Unlock()

	// 关闭旧连接（锁外，可能阻塞）
	if oldSvc != nil {
		oldSvc.Close()
	}

	// 创建新 Service（锁外，网络操作）
	tunnelLogger := newTunnelLogger(logFactory, token)
	newSvc, err := cloudflared.NewService(cloudflared.ServiceOptions{
		Token:            token,
		Logger:           tunnelLogger,
		ConnectionDialer: N.SystemDialer,
		GracePeriod:      5 * time.Second,
	})
	if err != nil {
		tunnelLogger.Error("watchdog tunnel create failed: ", err)
		return false
	}

	if err := newSvc.Start(); err != nil {
		tunnelLogger.Error("watchdog tunnel start failed: ", err)
		newSvc.Close()
		return false
	}

	// 成功后再次检查 context（取消则回滚）
	select {
	case <-ctx.Done():
		newSvc.Close()
		return false
	default:
	}

	// 发布新 Service
	mu.Lock()
	tunnelSvc = newSvc
	mu.Unlock()

	fmt.Fprintf(os.Stderr, "[sbx-watchdog] tunnel reconnected successfully\n")
	return true
}

// startTunnelWatchdog 监控 Tunnel 健康并在断连时自动重建
func startTunnelWatchdog(ctx context.Context, logFactory log.Factory) {
	defer close(watchdogDone)

	const probeInterval = 2 * time.Minute

	ticker := time.NewTicker(probeInterval)
	defer ticker.Stop()

	var recovery tunnelRecoveryState

	for {
		select {
		case <-ctx.Done():
			fmt.Fprintf(os.Stderr, "[sbx-watchdog] watchdog stopped\n")
			return

		case <-ticker.C:
			healthy := probeTunnelHealth()
			recordTunnelProbe(healthy)
			shouldReconnect := recovery.observeProbe(healthy)
			if !healthy {
				fmt.Fprintf(os.Stderr, "[sbx-watchdog] health check failed (%d/%d)\n", recovery.failures, tunnelFailureThreshold)
			}
			if !shouldReconnect {
				continue
			}

			// 触发重建
			if attemptReconnect(ctx, logFactory) {
				// 重建成功，重置计数
				recovery.reconnected()
				continue
			}

			// 重建失败，计算退避
			backoff := recovery.reconnectFailed()
			fmt.Fprintf(os.Stderr, "[sbx-watchdog] tunnel reconnect failed, retry in %v\n", backoff)

			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
				continue
			}
		}
	}
}

func main() {}
