// Package engine runs the embedded PowerShell bridge that talks to the
// Windows Runtime tethering APIs (NetworkOperatorTetheringManager and
// friends). Everything user-facing is Go; only the WinRT calls, which
// have no pure-Go bindings, ride on PowerShell 5.1 — which ships with
// every Windows this tool supports.
package engine

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed engine.ps1
var script string

// Info describes the machine as the engine sees it.
type Info struct {
	AdapterName   string
	AdapterDesc   string
	AdapterStatus string
	Uplink        string // connection profile shared from
	State         string // TetheringOperationalState: On / Off / ...
	SSID          string // currently configured SSID
	Clients       int
}

// Verdict is the outcome of a start-try or stop-wait.
type Verdict struct {
	OK        bool
	Already   bool   // start: hotspot was already On
	Nothing   bool   // stop: hotspot was not On
	State     string // manager state after the operation
	Fresh     string // fresh-manager state from the cross-check
	WFD       bool   // Wi-Fi Direct virtual adapter Up
	Broadcast bool   // the SSID is visible in the air
}

// result is the decoded engine answer.
type result map[string]any

func (r result) str(key string) string {
	if v, ok := r[key].(string); ok {
		return v
	}
	return ""
}

func (r result) bl(key string) bool {
	v, ok := r[key].(bool)
	return ok && v
}

func (r result) num(key string) int {
	switch v := r[key].(type) {
	case float64:
		return int(v)
	case string:
		n, _ := strconv.Atoi(v)
		return n
	default:
		return 0
	}
}

// Engine runs engine.ps1. The zero value is not usable; call New.
type Engine struct {
	// Log receives one line per invocation (op and duration), never
	// credentials.
	Log func(string)

	// run is the process seam; tests replace it.
	run runnerFunc

	scriptOnce sync.Once
	scriptPath string
	scriptErr  error
}

// New returns an engine using the real PowerShell bridge.
func New(log func(string)) *Engine {
	e := &Engine{Log: log}
	e.run = e.exec
	return e
}

// runnerFunc performs one engine invocation and returns stdout.
type runnerFunc func(ctx context.Context, envReq string) (string, error)

// Exec errors.
var (
	ErrNoAnswer = errors.New("引擎无有效输出")
	ErrFailed   = errors.New("引擎执行失败")
)

func (e *Engine) ensureScript() (string, error) {
	e.scriptOnce.Do(func() {
		dir, err := os.MkdirTemp("", "gzist-hotspot")
		if err != nil {
			e.scriptErr = err
			return
		}
		p := filepath.Join(dir, "engine.ps1")
		e.scriptErr = os.WriteFile(p, []byte(script), 0o600)
		if e.scriptErr == nil {
			e.scriptPath = p
		}
	})
	return e.scriptPath, e.scriptErr
}

// run performs one op and decodes the answer. req holds op-specific
// fields; op itself is added here.
func (e *Engine) run2(ctx context.Context, op string, fields map[string]any, timeout time.Duration) (result, error) {
	req := map[string]any{"op": op}
	for k, v := range fields {
		req[k] = v
	}
	b, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	start := time.Now()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	out, err := e.run(ctx, string(b))
	if e.Log != nil {
		e.Log(fmt.Sprintf("engine %s 用时 %s", op, time.Since(start).Round(100*time.Millisecond)))
	}
	if err != nil {
		return nil, err
	}
	r, err := decode(out)
	if err != nil {
		return nil, err
	}
	if !r.bl("ok") {
		msg := r.str("error")
		if msg == "" {
			msg = "引擎报告失败（ok=false）"
		}
		return r, fmt.Errorf("%w: %s", ErrFailed, msg)
	}
	return r, nil
}

// decode picks the last JSON object line from stdout.
func decode(stdout string) (result, error) {
	for _, line := range revLines(stdout) {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var r result
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			continue
		}
		return r, nil
	}
	return nil, ErrNoAnswer
}

func revLines(s string) []string {
	bom := "\ufeff"
	lines := strings.Split(strings.TrimPrefix(s, bom), "\n")
	for i, j := 0, len(lines)-1; i < j; i, j = i+1, j-1 {
		lines[i], lines[j] = lines[j], lines[i]
	}
	return lines
}

// Discover inspects the wireless NIC, the uplink profile and the current
// hotspot state.
func (e *Engine) Discover(ctx context.Context) (Info, error) {
	r, err := e.run2(ctx, "discover", nil, 40*time.Second)
	if err != nil {
		return Info{}, err
	}
	return Info{
		AdapterName:   r.str("nic_name"),
		AdapterDesc:   r.str("nic_desc"),
		AdapterStatus: r.str("nic_status"),
		Uplink:        r.str("uplink"),
		State:         r.str("state"),
		SSID:          r.str("ssid"),
		Clients:       r.num("clients"),
	}, nil
}

// Configure writes the SSID and passkey into the access point
// configuration and verifies the readback.
func (e *Engine) Configure(ctx context.Context, ssid, passkey string) error {
	_, err := e.run2(ctx, "configure", map[string]any{"ssid": ssid, "passkey": passkey}, 25*time.Second)
	return err
}

// StartTry fires StartTetheringAsync, polls for On and cross-checks live
// signals. It does NOT roll back; the caller decides.
func (e *Engine) StartTry(ctx context.Context, seconds int, ssid string) (Verdict, error) {
	r, err := e.run2(ctx, "start-try", map[string]any{"seconds": seconds, "ssid": ssid}, time.Duration(seconds+20)*time.Second)
	if err != nil {
		return Verdict{}, err
	}
	return Verdict{
		OK:        true,
		Already:   r.bl("already"),
		State:     r.str("state"),
		Fresh:     r.str("fresh"),
		WFD:       r.bl("wfd"),
		Broadcast: r.bl("broadcast"),
	}, nil
}

// StopWait fires StopTetheringAsync and polls for Off.
func (e *Engine) StopWait(ctx context.Context, seconds int) (Verdict, error) {
	r, err := e.run2(ctx, "stop-wait", map[string]any{"seconds": seconds}, time.Duration(seconds+20)*time.Second)
	if err != nil {
		return Verdict{}, err
	}
	return Verdict{
		OK:      true,
		Nothing: r.bl("nothing"),
		State:   r.str("state"),
	}, nil
}
