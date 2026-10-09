//go:build windows

package hotspot

import (
	"os/exec"
	"strings"
	"syscall"
)

// BroadcastVisible checks whether the SSID shows up in the air, by asking
// netsh what networks the wireless card sees right now.
func BroadcastVisible(iface, ssid string) bool {
	if iface == "" || ssid == "" {
		return false
	}
	cmd := exec.Command("netsh", "wlan", "show", "networks", "interface="+iface)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), ssid)
}
