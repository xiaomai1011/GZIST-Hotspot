// GZIST Hotspot turns a wired campus-network connection into a Wi-Fi
// hotspot. It lives in the tray (menu bar on macOS) and shows a small
// window on demand.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/xiaomai1011/GZIST-Hotspot/internal/applog"
	"github.com/xiaomai1011/GZIST-Hotspot/internal/art"
	"github.com/xiaomai1011/GZIST-Hotspot/internal/clash"
	"github.com/xiaomai1011/GZIST-Hotspot/internal/engine"
	"github.com/xiaomai1011/GZIST-Hotspot/internal/hotspot"
	"github.com/xiaomai1011/GZIST-Hotspot/internal/netcheck"
	"github.com/xiaomai1011/GZIST-Hotspot/internal/settings"
)

const appTitle = "GZIST Hotspot"

type app struct {
	log   *applog.Log
	store *settings.Store
	mgr   *hotspot.Manager
	m     *model

	win      *mygo.Window
	tray     *mygo.Tray
	menu     *mygo.Menu
	trayIcon art.TrayState
}

func main() {
	selftest := flag.Bool("selftest", false, "run a read-only self check and exit")
	flag.Parse()
	if *selftest {
		runSelftest()
		return
	}

	if !mygo.App.RequestSingleInstanceLock() {
		return // the running instance opens its window
	}
	a := &app{}
	mygo.App.OnSecondInstance(func([]string, string) { a.showWindow() })
	// Closing the window keeps the app running in the tray.
	mygo.App.OnWindowAllClosed(func() {})
	mygo.App.WhenReady(a.ready)
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}

func (a *app) ready() {
	if runtime.GOOS == "darwin" {
		mygo.App.SetActivationPolicy(mygo.ActivationPolicyAccessory)
	}
	dataDir, _ := mygo.App.Path(mygo.PathUserData)
	logDir, _ := mygo.App.Path(mygo.PathLogs)
	a.log = applog.New(logDir)
	a.store = settings.New(dataDir)
	a.log.Printf("GZIST Hotspot v%s 启动 (%s/%s)", mygo.App.Version(), runtime.GOOS, runtime.GOARCH)

	// First run: adopt the PowerShell version's hotspot_config.json if it
	// sits next to the executable or in the working directory.
	if msg := a.importLegacy(); msg != "" {
		a.log.Add(msg)
	}

	st := a.store.Load()
	pass, insecure, err := a.store.Passkey()
	if err != nil {
		a.log.Printf("读取密码失败: %v", err)
	}
	a.m = &model{
		ssid:       st.Ssid,
		pass:       pass,
		storedPass: pass,
		saved:      st.Ssid,
		hasPass:    pass != "",
		insecure:   insecure,
		autostart:  mygo.App.OpenAtLogin(),
		version:    mygo.App.Version(),
	}
	a.m.act = actions{
		Start:        a.start,
		Stop:         a.stop,
		Refresh:      a.refresh,
		Save:         a.save,
		SetAutostart: a.setAutostart,
		OpenLogs:     func() { mygo.Shell.ShowItemInFolder(a.log.Path()) },
	}

	cl := clash.New(a.log.Add)
	ck := netcheck.New()
	eng := engine.New(a.log.Add)
	a.mgr = hotspot.New(hotspot.Deps{
		Engine: eng,
		ClashTun: func(ctx context.Context) (hotspot.Tun, error) {
			t, err := cl.TunEnabled(ctx)
			return hotspot.Tun(t), err
		},
		ClashSet:  func(ctx context.Context, on bool) error { return cl.SetTun(ctx, on) },
		Inet:      ck.OKRetry,
		Broadcast: hotspot.BroadcastVisible,
		Log:       a.log.Add,
		Notify:    a.notify,
		OnChange: func(s hotspot.State) {
			a.post(func() {
				a.m.st = s
				a.syncTray()
			})
		},
	})
	a.log.OnChange(func() {
		lines := a.log.Lines()
		a.post(func() { a.m.logs = lines })
	})
	a.m.logs = a.log.Lines()

	a.makeTray()
	mygo.Power.OnResume(func() {
		a.log.Add("系统已唤醒，刷新热点状态")
		a.mgr.Refresh()
	})
	go a.mgr.Run(context.Background())

	if st.Ssid == "" || pass == "" {
		a.showWindow() // first run: ask for the credentials
	} else if !mygo.App.WasOpenedAtLogin() {
		a.showWindow() // launched by hand: show the window once
	}
}

// importLegacy adopts the PowerShell version's hotspot_config.json. It
// returns a log line, or "" when there was nothing to import.
func (a *app) importLegacy() string {
	st := a.store.Load()
	pass, _, err := a.store.Passkey()
	if err == nil && st.Ssid != "" && pass != "" {
		return "" // already configured
	}
	var candidates []string
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), "hotspot_config.json"))
	}
	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(cwd, "hotspot_config.json"))
	}
	for _, p := range candidates {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		lg, err := settings.ParseLegacyConfig(b)
		if err != nil {
			return "旧配置 " + p + " 存在但无法解析: " + err.Error()
		}
		if lg.Ssid == "" || lg.Passkey == "" {
			continue
		}
		if err := a.store.Save(settings.Settings{Ssid: lg.Ssid}); err != nil {
			return "导入旧配置失败: " + err.Error()
		}
		if _, err := a.store.SetPasskey(lg.Passkey); err != nil {
			return "导入旧配置的密码失败: " + err.Error()
		}
		return "已从旧版 hotspot_config.json 导入热点配置（" + p + "）"
	}
	return ""
}

// post runs fn on the main thread and redraws the window.
func (a *app) post(fn func()) {
	mygo.RunOnMain(func() {
		fn()
		if a.win != nil && !a.win.IsDestroyed() {
			a.win.Invalidate()
		}
	})
}

func (a *app) showWindow() {
	if a.win != nil && !a.win.IsDestroyed() {
		a.win.Show()
		a.win.Focus()
		mygo.App.Focus()
		return
	}
	a.win = mygo.NewWindow(mygo.WindowOptions{
		Title:     appTitle,
		Width:     440,
		Height:    780,
		MinWidth:  380,
		MinHeight: 520,
		StateKey:  "main",
		Content:   ui.View(a.m.view),
	})
	mygo.App.Focus()
}

func (a *app) makeTray() {
	a.menu = mygo.NewMenu([]*mygo.MenuItem{
		{ID: "status", Label: "检测中…", Disabled: true},
		mygo.Separator(),
		{ID: "start", Label: "开热点", Click: func(*mygo.MenuItem, *mygo.Window) { a.start() }},
		{ID: "stop", Label: "关热点", Click: func(*mygo.MenuItem, *mygo.Window) { a.stop() }},
		{Label: "打开主窗口", Click: func(*mygo.MenuItem, *mygo.Window) { a.showWindow() }},
		mygo.Separator(),
		{ID: "autostart", Label: "开机自启", Type: mygo.MenuItemCheckbox, Checked: a.m.autostart,
			Click: func(it *mygo.MenuItem, _ *mygo.Window) { a.setAutostart(it.Checked) }},
		mygo.Separator(),
		{Label: "退出", Click: func(*mygo.MenuItem, *mygo.Window) { mygo.App.Quit() }},
	})
	a.trayIcon = art.TrayOff
	tray, err := mygo.NewTray(mygo.TrayOptions{
		Icon:           art.TrayIcon(art.TrayOff, runtime.GOOS == "darwin"),
		IconIsTemplate: runtime.GOOS == "darwin",
		ToolTip:        appTitle,
		Menu:           a.menu,
	})
	if err != nil {
		a.log.Printf("无法创建托盘图标（Linux 需要安装 libayatana-appindicator3）: %v", err)
		return
	}
	a.tray = tray
	if runtime.GOOS == "windows" {
		tray.OnClick(a.showWindow)
	}
}

// syncTray mirrors the model into the tray. Main thread.
func (a *app) syncTray() {
	if a.tray == nil {
		return
	}
	s := describe(a.m.st)
	label := "热点：" + s.title
	if a.m.st.Phase == hotspot.On && a.m.st.Clients > 0 {
		label += fmt.Sprintf("（%d 台设备）", a.m.st.Clients)
	}
	a.menu.ItemByID("status").SetLabel(label)
	busy := a.m.st.Phase.Busy()
	a.menu.ItemByID("start").SetEnabled(!busy && a.m.saved != "")
	a.menu.ItemByID("stop").SetEnabled(!busy)
	a.menu.ItemByID("autostart").SetChecked(a.m.autostart)
	a.tray.SetToolTip(appTitle + " · " + s.title)
	if s.tray != a.trayIcon {
		a.trayIcon = s.tray
		mac := runtime.GOOS == "darwin"
		a.tray.SetIcon(art.TrayIcon(s.tray, mac), mac)
	}
}

func (a *app) notify(title, body string) {
	if !mygo.NotificationsSupported() {
		return
	}
	mygo.RunOnMain(func() {
		n := mygo.NewNotification(mygo.NotificationOptions{Title: title, Body: body})
		n.OnClick(a.showWindow)
		if err := n.Show(); err != nil {
			a.log.Printf("通知发送失败: %v", err)
		}
	})
}

func (a *app) start() {
	if a.m.st.Phase.Busy() {
		return
	}
	ssid := a.m.saved
	if ssid == "" {
		a.m.notice = "请先在设置里保存热点名称和密码"
		a.post(func() { a.syncTray() })
		return
	}
	pass, _, err := a.store.Passkey()
	if err != nil {
		a.log.Printf("读取密码失败: %v", err)
	}
	a.mgr.Start(ssid, pass)
}

func (a *app) stop() {
	if a.m.st.Phase.Busy() {
		return
	}
	a.mgr.Stop()
}

func (a *app) refresh() {
	if a.m.st.Phase.Busy() {
		return
	}
	a.mgr.Refresh()
}

// save stores new credentials. The passkey field is always filled once a
// password exists; saving an empty field deletes the passkey. Main
// thread; the keyring work runs aside.
func (a *app) save(ssid, pass string) {
	old := a.m.saved
	if ssid == "" {
		a.m.notice = "热点名称不能为空"
		return
	}
	if ssid != old && pass == "" && !a.m.hasPass {
		a.m.notice = "第一次设置请同时填写密码"
		return
	}
	a.m.notice = "保存中…"
	a.post(func() {})
	go func() {
		insecure := a.m.insecure
		var err error
		if pass == "" {
			a.store.DeletePasskey()
			insecure = false
		} else if pass != a.m.storedPass {
			insecure, err = a.store.SetPasskey(pass)
		}
		if err == nil {
			err = a.store.Save(settings.Settings{Ssid: ssid})
		}
		if err != nil {
			a.log.Printf("保存失败: %v", err)
			a.post(func() { a.m.notice = "保存失败：" + err.Error() })
			return
		}
		a.log.Printf("热点配置已保存（SSID=%s）", ssid)
		if insecure {
			a.log.Add("系统凭据管理器不可用，密码以明文（权限 0600）保存在本机")
		}
		a.post(func() {
			a.m.saved, a.m.pass, a.m.storedPass = ssid, pass, pass
			a.m.hasPass, a.m.insecure = pass != "", insecure
			a.m.notice = "已保存"
			a.syncTray()
		})
	}()
}

func (a *app) setAutostart(on bool) {
	if err := mygo.App.SetOpenAtLogin(on); err != nil {
		a.log.Printf("设置开机自启失败: %v", err)
		a.m.notice = "开机自启设置失败：" + err.Error()
	} else if on {
		a.log.Add("已开启开机自启")
	} else {
		a.log.Add("已关闭开机自启")
	}
	a.m.autostart = mygo.App.OpenAtLogin()
	a.syncTray()
	if a.win != nil && !a.win.IsDestroyed() {
		a.win.Invalidate()
	}
}

// runSelftest is a read-only check for installers and debugging: it
// prints the stored settings, the Clash TUN state and the hotspot state,
// then exits. It never touches the hotspot.
func runSelftest() {
	fmt.Println("== GZIST Hotspot 自检（只读）==")
	base, err := os.UserConfigDir()
	var dir string
	if err != nil {
		dir = "."
	} else {
		dir = filepath.Join(base, "GZIST Hotspot")
	}
	fmt.Println("数据目录:", dir)
	st := settings.New(dir).Load()
	fmt.Printf("settings: ssid=%q\n", st.Ssid)
	if pass, _, err := settings.New(dir).Passkey(); err != nil {
		fmt.Println("passkey: 读取失败:", err)
	} else if pass == "" {
		fmt.Println("passkey: 未设置")
	} else {
		fmt.Printf("passkey: 已设置（长度 %d）\n", len(pass))
	}

	logf := func(s string) { fmt.Println("  " + s) }
	cl := clash.New(logf)
	t, err := cl.TunEnabled(context.Background())
	if err != nil {
		fmt.Println("clash TUN: unknown (api fail):", err)
	} else {
		fmt.Println("clash TUN:", t)
	}

	eng := engine.New(logf)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	info, err := eng.Discover(ctx)
	if err != nil {
		fmt.Println("hotspot discover 失败:", err)
		return
	}
	fmt.Printf("adapter: %s [%s] (%s)\n", info.AdapterName, info.AdapterDesc, info.AdapterStatus)
	fmt.Println("uplink:", info.Uplink)
	fmt.Printf("hotspot: state=%s ssid=[%s] clients=%d\n", info.State, info.SSID, info.Clients)
	fmt.Println("== 自检完成 ==")
}
