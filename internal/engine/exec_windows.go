//go:build windows

package engine

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
)

// exec runs powershell.exe on the embedded script. The request rides in
// an environment variable (Unicode-safe, invisible to process listings)
// and the answer comes back as JSON on stdout.
func (e *Engine) exec(ctx context.Context, envReq string) (string, error) {
	path, err := e.ensureScript()
	if err != nil {
		return "", fmt.Errorf("准备引擎脚本失败: %w", err)
	}
	cmd := exec.CommandContext(ctx, "powershell",
		"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", path)
	cmd.Env = append(os.Environ(), "HOTSPOT_REQ="+envReq)
	// The app is a GUI process; without CREATE_NO_WINDOW each call would
	// flash a console.
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if ctx.Err() != nil {
		return "", fmt.Errorf("引擎超时或被取消: %w", ctx.Err())
	}
	if err != nil {
		if msg := clip(strings.TrimSpace(stderr.String()), 300); msg != "" {
			return "", fmt.Errorf("引擎进程失败: %v: %s", err, msg)
		}
		return "", fmt.Errorf("引擎进程失败: %w", err)
	}
	return string(out), nil
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
