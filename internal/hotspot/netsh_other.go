//go:build !windows

package hotspot

// BroadcastVisible is only meaningful on Windows.
func BroadcastVisible(iface, ssid string) bool { return false }
