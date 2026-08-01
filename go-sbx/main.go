package main

import "C"
import (
	"context"
	"fmt"
	"os"
	"sync"

	box "github.com/sagernet/sing-box"
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
			Logger:           instance.LogFactory().NewLogger("cloudflared"),
			ConnectionDialer: N.SystemDialer,
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

	// stateRunning: take ownership of the instances, then close them with the
	// lock released so a slow Close never blocks a concurrent caller.
	svc, inst, cancel := tunnelSvc, boxInst, cancelFn
	tunnelSvc, boxInst, cancelFn = nil, nil, nil
	state = stateIdle
	mu.Unlock()

	// Shutdown order per design: tunnel first, then sing-box, then context.
	// Both Close errors are reported rather than swallowed, but neither aborts
	// the sequence — the context must be cancelled either way.
	var failed bool
	if svc != nil {
		if err := svc.Close(); err != nil {
			failed = true
			fmt.Fprintf(os.Stderr, "[sbx] tunnel close error: %v\n", err)
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

func main() {}
