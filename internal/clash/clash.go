//go:build windows

// Package clash talks to the mihomo (Clash Verge) control API over its
// named pipe, the only channel that works without the GUI: it reads and
// flips the TUN switch. The hotspot and TUN are mutually exclusive, so
// starting the hotspot turns TUN off first.
package clash

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

// Tun is the TUN switch state as far as we can see.
type Tun int

const (
	TunUnknown Tun = iota // the API could not be reached
	TunOff
	TunOn
)

func (t Tun) String() string {
	switch t {
	case TunOn:
		return "enabled"
	case TunOff:
		return "disabled"
	default:
		return "unknown"
	}
}

// PipeNames lists the named pipes on this machine whose names match
// want. The order of the result follows the system.
func PipeNames() ([]string, error) {
	var names []string
	data := &windows.Win32finddata{}
	h, err := windows.FindFirstFile(windows.StringToUTF16Ptr(`\\.\pipe\*`), data)
	if err != nil {
		return nil, err
	}
	defer windows.FindClose(h)
	for {
		names = append(names, windows.UTF16ToString(data.FileName[:]))
		if err := windows.FindNextFile(h, data); err != nil {
			if err == windows.ERROR_NO_MORE_FILES {
				break
			}
			return names, err
		}
	}
	return names, nil
}

// FindPipe picks the mihomo control pipe. Clash Verge 2.5.4+ hosts the
// kernel in its service and exposes
// \\.\pipe\verge-mihomo-production-<hash>; older builds used
// \\.\pipe\verge-mihomo. The -sidecar-release- pipe exists in the config
// but does not answer.
func FindPipe() string {
	names, err := PipeNames()
	if err != nil {
		return ""
	}
	var loose string
	for _, n := range names {
		switch {
		case strings.Contains(n, "verge-mihomo-production"):
			return `\\.\pipe\` + n
		case strings.Contains(n, "verge-mihomo"), strings.Contains(n, "mihomo"):
			if !strings.Contains(n, "sidecar") && loose == "" {
				loose = n
			}
		}
	}
	if loose != "" {
		return `\\.\pipe\` + loose
	}
	return ""
}

// Client talks to the mihomo control API. The zero value is not usable;
// call New.
type Client struct {
	// Pipe is the named pipe path; empty means "look one up per call".
	Pipe string
	// Log receives one line per notable event.
	Log func(string)

	client *http.Client
}

// New returns a client. If it cannot see a control pipe now it will look
// again on every call (Clash may not be running yet).
func New(log func(string)) *Client {
	c := &Client{Pipe: FindPipe(), Log: log}
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		pipe := c.Pipe
		if pipe == "" {
			pipe = FindPipe()
			if pipe == "" {
				return nil, fmt.Errorf("mihomo 控制管道未找到（Clash Verge 在运行吗？）")
			}
			c.Pipe = pipe
			c.logf("找到 mihomo 控制管道: %s", pipe)
		}
		t := 4 * time.Second
		return winio.DialPipe(pipe, &t)
	}
	c.client = &http.Client{
		Transport: &http.Transport{
			DialContext:       dial,
			DisableKeepAlives: true,
		},
	}
	return c
}

func (c *Client) logf(format string, args ...any) {
	if c.Log != nil {
		c.Log(fmt.Sprintf(format, args...))
	}
}

// call performs one API request and returns the status code and body.
func (c *Client) call(ctx context.Context, method, path, body string) (int, []byte, error) {
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://mihomo"+path, rd)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer set-your-secret") // mihomo on the pipe has no secret
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		// The pipe may have vanished (Clash quit): forget it and retry once.
		c.Pipe = ""
		return 0, nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, b, err
}

// TunEnabled reads the TUN switch. TunUnknown with an error when the API
// cannot be reached (Clash not running) — never an obstacle by itself.
func (c *Client) TunEnabled(ctx context.Context) (Tun, error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	status, body, err := c.call(ctx, http.MethodGet, "/configs", "")
	if err != nil {
		return TunUnknown, err
	}
	if status != http.StatusOK {
		return TunUnknown, fmt.Errorf("GET /configs: HTTP %d", status)
	}
	var j struct {
		Tun struct {
			Enable bool `json:"enable"`
		} `json:"tun"`
	}
	if err := json.Unmarshal(body, &j); err != nil {
		return TunUnknown, err
	}
	if j.Tun.Enable {
		return TunOn, nil
	}
	return TunOff, nil
}

// SetTun flips the TUN switch. It reports success the same way the
// PowerShell version did: any 200-series answer counts.
func (c *Client) SetTun(ctx context.Context, on bool) error {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	b := `{"tun":{"enable":true}}`
	if !on {
		b = `{"tun":{"enable":false}}`
	}
	status, _, err := c.call(ctx, http.MethodPatch, "/configs", b)
	if err != nil {
		return err
	}
	if status < 200 || status > 204 {
		return fmt.Errorf("PATCH /configs: HTTP %d", status)
	}
	return nil
}
