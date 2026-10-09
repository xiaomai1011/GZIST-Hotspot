package main

import (
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/xiaomai1011/GZIST-Hotspot/internal/hotspot"
)

type recorder struct {
	starts, stops, refreshes int
	saved                    [][2]string
	autostart                []bool
}

func newModel(r *recorder) *model {
	return &model{
		version: "2.0.0",
		act: actions{
			Start:        func() { r.starts++ },
			Stop:         func() { r.stops++ },
			Refresh:      func() { r.refreshes++ },
			Save:         func(s, p string) { r.saved = append(r.saved, [2]string{s, p}) },
			SetAutostart: func(on bool) { r.autostart = append(r.autostart, on) },
			OpenLogs:     func() {},
		},
	}
}

func TestOffShowsDetails(t *testing.T) {
	r := &recorder{}
	m := newModel(r)
	m.saved, m.hasPass = "my-ap", true
	m.st = hotspot.State{
		Phase: hotspot.Off, HasAP: true,
		Adapter: "WLAN [Test Wi-Fi 6E] (Up)", AdapterName: "WLAN",
		Uplink: "Ethernet", SSID: "my-ap",
		Tun: hotspot.TunOff, InetKnown: true, InetOK: true,
	}
	tt := ui.NewTester(m.view, 440, 780)
	for _, want := range []string{"热点未开启", "WLAN [Test Wi-Fi 6E] (Up)", "Ethernet", "my-ap", "OK", "关闭"} {
		if !tt.HasText(want) {
			t.Fatalf("missing %q in %q", want, tt.Texts())
		}
	}
	if err := tt.Click("刷新"); err != nil {
		t.Fatal(err)
	}
	if r.refreshes != 1 {
		t.Fatal("refresh not clicked")
	}
}

func TestFirstRunDisablesStart(t *testing.T) {
	r := &recorder{}
	m := newModel(r)
	tt := ui.NewTester(m.view, 440, 780)
	tt.Click("开热点")
	if r.starts != 0 {
		t.Fatal("start clicked without saved ssid")
	}
	if !tt.HasText("还没检测到热点环境") {
		t.Fatalf("texts %q", tt.Texts())
	}
}

func TestBusyDisablesButtons(t *testing.T) {
	r := &recorder{}
	m := newModel(r)
	m.saved = "my-ap"
	m.st = hotspot.State{Phase: hotspot.Starting}
	tt := ui.NewTester(m.view, 440, 780)
	tt.Click("开热点")
	tt.Click("关热点")
	if r.starts != 0 || r.stops != 0 {
		t.Fatal("clicked while busy")
	}
	if !tt.HasText("正在开启热点…") {
		t.Fatalf("texts %q", tt.Texts())
	}
}

func TestOnShowsClients(t *testing.T) {
	m := newModel(&recorder{})
	m.saved = "my-ap"
	m.st = hotspot.State{Phase: hotspot.On, HasAP: true, Clients: 3, SSID: "my-ap", Tun: hotspot.TunOff}
	tt := ui.NewTester(m.view, 440, 780)
	if !tt.HasText("热点开启中") || !tt.HasText("3 台") {
		t.Fatalf("texts %q", tt.Texts())
	}
}

func TestSaveCredentials(t *testing.T) {
	r := &recorder{}
	m := newModel(r)
	tt := ui.NewTester(m.view, 440, 780)
	if err := tt.Click("热点名称"); err != nil {
		t.Fatal(err)
	}
	tt.Type("my-ap")
	if err := tt.Click("密码"); err != nil {
		t.Fatal(err)
	}
	tt.Type("secret")
	if err := tt.Click("保存"); err != nil {
		t.Fatal(err)
	}
	if len(r.saved) != 1 || r.saved[0] != [2]string{"my-ap", "secret"} {
		t.Fatalf("saved %v", r.saved)
	}
	for _, s := range tt.Texts() {
		if s == "secret" {
			t.Fatal("password shown in clear")
		}
	}
}

func TestAutostartSwitch(t *testing.T) {
	r := &recorder{}
	m := newModel(r)
	tt := ui.NewTester(m.view, 440, 780)
	if err := tt.Click("开机自启"); err != nil {
		t.Fatal(err)
	}
	if len(r.autostart) != 1 || !r.autostart[0] {
		t.Fatalf("autostart %v", r.autostart)
	}
}

// TestScreenshots writes previews when HOTSPOT_SHOTS names a directory.
func TestScreenshots(t *testing.T) {
	dir := hotspotShotsDir()
	if dir == "" {
		t.Skip("set HOTSPOT_SHOTS to write screenshots")
	}
	logs := []string{
		"12:00:01  GZIST Hotspot v2.0.0 启动 (windows/amd64)",
		"12:00:02  adapter: WLAN [MediaTek Wi-Fi 6E MT7922] (Up)",
		"12:00:02  uplink profile: 以太网",
		"12:00:02  hotspot: state=Off ssid=[xiaomai-AP] clients=0",
		"12:00:02  clash TUN: enabled",
		"12:00:03  STEP1 关闭 Clash TUN …",
		"12:00:09  STEP1 完成：TUN 已关，直连正常",
		"12:00:12  STEP3 结果: state=On OK",
		"12:00:15  FINAL: 热点开启 + TUN 关闭 + 直连正常",
	}
	shots := map[string]func(m *model){
		"on": func(m *model) {
			m.saved, m.hasPass, m.pass, m.storedPass = "xiaomai-AP", true, "xiaomai2026", "xiaomai2026"
			m.st = hotspot.State{Phase: hotspot.On, HasAP: true, Clients: 2,
				Adapter: "WLAN [MediaTek Wi-Fi 6E MT7922] (Up)", AdapterName: "WLAN",
				Uplink: "以太网", SSID: "xiaomai-AP", Tun: hotspot.TunOff, InetKnown: true, InetOK: true}
			m.autostart, m.logs = true, logs
		},
		"first-run": func(m *model) { m.logs = logs[:4] },
		"starting": func(m *model) {
			m.saved, m.hasPass, m.pass, m.storedPass = "xiaomai-AP", true, "xiaomai2026", "xiaomai2026"
			m.st = hotspot.State{Phase: hotspot.Starting, HasAP: true,
				Adapter: "WLAN [MediaTek Wi-Fi 6E MT7922] (Up)", AdapterName: "WLAN",
				Uplink: "以太网", SSID: "xiaomai-AP", Tun: hotspot.TunOff}
			m.logs = logs[:8]
		},
		"failed": func(m *model) {
			m.saved, m.hasPass, m.pass, m.storedPass = "xiaomai-AP", true, "xiaomai2026", "xiaomai2026"
			m.st = hotspot.State{Phase: hotspot.Off, HasAP: true,
				LastError: "启动热点失败：45 秒内未进入 On 且实时信号均未命中",
				Adapter: "WLAN [MediaTek Wi-Fi 6E MT7922] (Up)", AdapterName: "WLAN",
				Uplink: "以太网", SSID: "xiaomai-AP", Tun: hotspot.TunOn}
			m.logs = logs[:8]
		},
	}
	for name, setup := range shots {
		for _, dark := range []bool{false, true} {
			m := newModel(&recorder{})
			setup(m)
			tt := ui.NewTester(m.view, 440, 780)
			tt.SetScale(2)
			tt.SetDark(dark)
			tt.Frame()
			suffix := ""
			if dark {
				suffix = "-dark"
			}
			writeShot(t, dir, name+suffix+".png", tt)
		}
	}
}
