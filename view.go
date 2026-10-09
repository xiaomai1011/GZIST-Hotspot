package main

import (
	"fmt"
	"math"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/xiaomai1011/GZIST-Hotspot/internal/art"
	"github.com/xiaomai1011/GZIST-Hotspot/internal/hotspot"
)

// The look follows BanG Dream! It's MyGO!!!!! in Rana's green: the five
// members' colors, a 迷星 for the status and 乐奈的吉他盒 for the log.
var (
	colBand   = ui.Hex("#4CAF6D") // Rana's green, deepened for contrast
	colTomori = ui.Hex("#77BBDD")
	colAnon   = ui.Hex("#FF8899")
	colRana   = ui.Hex("#77DD77")
	colSoyo   = ui.Hex("#FFDD88")
	colTaki   = ui.Hex("#7777AA")
	members   = []ui.Color{colTomori, colAnon, colRana, colSoyo, colTaki}
)

// actions are what the window can ask the app to do. Tests replace them.
type actions struct {
	Start        func()
	Stop         func()
	Refresh      func()
	Save         func(ssid, pass string)
	SetAutostart func(on bool)
	OpenLogs     func()
}

// model is everything the main window shows. It is only touched on the
// main thread.
type model struct {
	st         hotspot.State
	ssid       string // text field
	pass       string // text field, always filled once a passkey exists
	storedPass string // what is on disk; the field starts with it
	showPass   bool   // the eye toggle: true = plain text
	saved      string // ssid on disk
	hasPass    bool
	insecure   bool
	autostart  bool
	notice     string // result of the last save
	logs       []string
	logScroll  ui.ScrollState
	version    string
	act        actions
}

// status describes the current phase in words and a member's color.
type status struct {
	title, line string
	color       ui.Color
	tray        art.TrayState
}

func describe(st hotspot.State) status {
	switch {
	case st.Phase == hotspot.Starting:
		return status{"正在开启热点…", "迷星叫 —— 正在点亮信号", colAnon, art.TrayBusy}
	case st.Phase == hotspot.Stopping:
		return status{"正在关闭热点…", "下次见", colAnon, art.TrayBusy}
	case st.Phase == hotspot.Checking:
		return status{"检测中…", "正在确认热点与网络状态", colTomori, art.TrayBusy}
	case st.Phase == hotspot.On:
		line := "碧天伴走 —— 信号已就位，直接连吧"
		if st.Clients > 0 {
			line = fmt.Sprintf("已有 %d 台设备连上来一起组乐队", st.Clients)
		}
		return status{"热点开启中", line, colBand, art.TrayOn}
	case !st.HasAP:
		return status{"还没检测到热点环境", "确认电脑有无线网卡（需支持 Wi-Fi Direct），点「刷新」重试", colSoyo, art.TrayOff}
	case st.LastError != "":
		return status{"热点未开启", "迷子中… " + st.LastError, colTaki, art.TrayError}
	case st.Phase == hotspot.Off:
		return status{"热点未开启", "点「开热点」，把有线网络分享给身边的设备", colRana, art.TrayOff}
	default:
		return status{"状态未知", "点「刷新」确认热点状态", colTaki, art.TrayOff}
	}
}

func applyTheme(c *ui.Context) *ui.Theme {
	t := *c.Theme()
	if t.Dark {
		t.Background = ui.Hex("#0F1A14")
		t.Surface, t.SurfaceHover, t.SurfacePressed = ui.Hex("#16281D"), ui.Hex("#1D3325"), ui.Hex("#24402E")
		t.Border = ui.Hex("#26402F")
		t.Text, t.TextMuted = ui.Hex("#E7F6EC"), ui.Hex("#8FAF9C")
		t.Accent, t.AccentHover, t.AccentPressed = ui.Hex("#4CAF6D"), ui.Hex("#66D08E"), ui.Hex("#3B8F55")
		t.Selection, t.Focus = colBand.Alpha(0.45), colRana.Alpha(0.6)
	} else {
		t.Background = ui.Hex("#EEF9F1")
		t.Surface, t.SurfaceHover, t.SurfacePressed = ui.Hex("#F3FBF6"), ui.Hex("#E5F5EC"), ui.Hex("#D8EDDF")
		t.Border = ui.Hex("#CBE3D4")
		t.Text, t.TextMuted = ui.Hex("#1A2E22"), ui.Hex("#63806E")
		t.Accent, t.AccentHover, t.AccentPressed = colBand, ui.Hex("#2E8B57"), ui.Hex("#246B3D")
		t.Selection, t.Focus = colBand.Alpha(0.25), colBand.Alpha(0.5)
	}
	t.AccentText = ui.Hex("#FFFFFF")
	t.Radius = 8
	c.SetTheme(&t)
	return &t
}

func cardBG(t *ui.Theme) ui.Color {
	if t.Dark {
		return ui.Hex("#122218")
	}
	return ui.Hex("#FFFFFF")
}

func card(c *ui.Context, t *ui.Theme) ui.Element {
	e := ui.Column(c).Padding(14).Gap(10).Radius(14).Background(cardBG(t)).Border(1, t.Border)
	if !t.Dark {
		e.Shadow(0, 2, 10, 0, ui.RGBA(30, 70, 110, 0.07))
	}
	return e
}

// starPath is a five-pointed star in r.
func starPath(r ui.Rect, inner float64, rot float64) *ui.Path {
	cx, cy := float64(r.X+r.W/2), float64(r.Y+r.H/2)+float64(r.H)*0.03
	pts := art.StarPoints(cx, cy, float64(min(r.W, r.H))/2, inner, rot)
	var p ui.Path
	p.MoveTo(float32(pts[0][0]), float32(pts[0][1]))
	for _, q := range pts[1:] {
		p.LineTo(float32(q[0]), float32(q[1]))
	}
	p.Close()
	return &p
}

func (m *model) view(c *ui.Context) {
	t := applyTheme(c)
	s := describe(m.st)
	ui.Column(c).Fill().Children(func() {
		m.header(c, t)
		ui.Scroll(c).Grow(1).Children(func() {
			ui.Column(c).Padding(14).Gap(12).Children(func() {
				m.statusCard(c, t, s)
				m.settingsCard(c, t)
				m.switches(c, t)
				m.logCard(c, t)
				m.footer(c, t)
			})
		})
	})
}

func (m *model) header(c *ui.Context, t *ui.Theme) {
	ui.Column(c).FillWidth().Children(func() {
		ui.Row(c).FillWidth().Padding(16, 18, 14, 18).Gap(12).AlignItems(ui.Center).
			Gradient(headerColors(t)).
			Draw(func(p *ui.Painter, r ui.Rect) {
				// sparkles scattered over the band's blue
				for _, s := range [][3]float32{{0.62, 0.22, 7}, {0.74, 0.70, 5}, {0.86, 0.30, 9}, {0.95, 0.75, 4}, {0.52, 0.78, 4}} {
					sr := ui.Rect{X: r.X + r.W*s[0] - s[2], Y: r.Y + r.H*s[1] - s[2], W: 2 * s[2], H: 2 * s[2]}
					p.FillPath(starPath(sr, 0.42, 0), ui.RGBA(255, 255, 255, 0.35))
				}
			}).
			Children(func() {
				ui.Box(c).Size(34, 34).Draw(func(p *ui.Painter, r ui.Rect) {
					p.FillPath(starPath(r, 0.48, -8), ui.Hex("#FFFFFF"))
				})
				ui.Column(c).Gap(2).Grow(1).Children(func() {
					ui.Text(c, "GZIST Hotspot").FontSize(19).Bold().TextColor(ui.Hex("#FFFFFF"))
					ui.Text(c, "迷子でも、つながる。").FontSize(11.5).TextColor(ui.RGBA(255, 255, 255, 0.85))
				})
			})
		// the five members, side by side
		ui.Row(c).FillWidth().Height(4).Children(func() {
			for _, col := range members {
				ui.Box(c).Grow(1).Height(4).Background(col)
			}
		})
	})
}

func headerColors(t *ui.Theme) (ui.Color, ui.Color, float32) {
	if t.Dark {
		return ui.Hex("#1F6B40"), ui.Hex("#3B9B55"), 120
	}
	return colBand, ui.Hex("#7FD9A0"), 120
}

func (m *model) statusCard(c *ui.Context, t *ui.Theme, s status) {
	card(c, t).Border(1.5, s.color.Alpha(0.75)).Gradient(cardBG(t).Mix(s.color, 0.16), cardBG(t), 180).Children(func() {
		ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
			star := ui.Box(c).Size(40, 40)
			busy := m.st.Phase.Busy() || m.st.Phase == hotspot.Checking
			if busy {
				star.Rotate(star.Loop("spin", 2400*time.Millisecond, ui.Linear) * 72)
			}
			on := m.st.Phase == hotspot.On
			col := s.color
			star.Draw(func(p *ui.Painter, r ui.Rect) {
				if on || busy {
					p.FillPath(starPath(r, 0.48, 0), col)
				} else {
					p.StrokePath(starPath(ui.Rect{X: r.X + 2, Y: r.Y + 2, W: r.W - 4, H: r.H - 4}, 0.48, 0), 2.5, col)
				}
			})
			ui.Column(c).Gap(3).Grow(1).Children(func() {
				ui.Text(c, s.title).FontSize(20).Bold()
				ui.Text(c, s.line).FontSize(12).TextColor(t.TextMuted).Wrap()
			})
		})
		if m.st.LastError != "" && m.st.Phase != hotspot.On {
			ui.Text(c, m.st.LastError).
				FontSize(12).Wrap().Padding(8, 10).Radius(8).Background(colSoyo.Alpha(0.22))
		}
		tun := "—"
		switch m.st.Tun {
		case hotspot.TunOn:
			tun = "开启"
			if m.st.TunTurnedOff {
				tun = "开启（异常）"
			}
		case hotspot.TunOff:
			tun = "关闭"
			if m.st.Phase == hotspot.On {
				tun += "（热点期间的正常状态）"
			}
		default:
			tun = "未知（Clash API 不可达）"
		}
		inet := "—"
		if m.st.InetKnown {
			inet = "OK"
			if !m.st.InetOK {
				inet = "FAIL"
			}
		}
		m.kvGrid(c, t, [][2]string{
			{"网卡", dash(m.st.Adapter)},
			{"上行", dash(m.st.Uplink)},
			{"SSID", dash(m.st.SSID)},
			{"客户端", clients(m.st.Clients)},
			{"TUN", tun},
			{"联网检测", inet},
		})
		ui.Row(c).Gap(8).Children(func() {
			busy := m.st.Phase.Busy()
			if ui.PrimaryButton(c, "开热点").Grow(1).Disabled(busy || m.saved == "").Clicked() {
				m.act.Start()
			}
			if ui.Button(c, "关热点").Grow(1).Disabled(busy).Clicked() {
				m.act.Stop()
			}
			if ui.Button(c, "刷新").Grow(1).Disabled(busy).Clicked() {
				m.act.Refresh()
			}
		})
	})
}

func clients(n int) string {
	if n <= 0 {
		return "—"
	}
	return fmt.Sprintf("%d 台", n)
}

// kvGrid lays out label/value pairs in two columns.
func (m *model) kvGrid(c *ui.Context, t *ui.Theme, kvs [][2]string) {
	ui.Grid(c).ColumnTracks(ui.Fixed(56), ui.Fr(1)).GapX(12).GapY(4).Children(func() {
		for _, kv := range kvs {
			ui.Text(c, kv[0]).FontSize(12).TextColor(t.TextMuted)
			ui.Text(c, kv[1]).FontSize(12).Selectable().Wrap()
		}
	})
}

func dash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func sectionTitle(c *ui.Context, t *ui.Theme, title string, col ui.Color) {
	ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Box(c).Size(8, 8).Radius(4).Background(col)
		ui.Text(c, title).FontSize(13).Bold()
	})
}

func (m *model) settingsCard(c *ui.Context, t *ui.Theme) {
	card(c, t).Children(func() {
		sectionTitle(c, t, "热点设置", colAnon)
		ui.TextInput(c, &m.ssid).Label("热点名称").Placeholder("热点名称").FillWidth()
		placeholder := "至少 8 位"
		if m.hasPass {
			placeholder = "清空后保存 = 删除密码"
		}
		var submitted bool
		if m.showPass {
			pw := ui.TextInput(c, &m.pass).Label("密码").FillWidth()
			pw.Placeholder(placeholder)
			submitted = pw.Submitted()
		} else {
			pw := ui.TextInput(c, &m.pass).Label("密码").Password().FillWidth()
			pw.Placeholder(placeholder)
			submitted = pw.Submitted()
		}
		eye := "👁 显示"
		if m.showPass {
			eye = "🙈 隐藏"
		}
		ui.Row(c).Gap(8).AlignItems(ui.Center).FillWidth().Children(func() {
			if ui.Button(c, eye).FontSize(12).Clicked() {
				m.showPass = !m.showPass
			}
		})
		ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
			msg := m.notice
			if msg == "" && m.insecure {
				msg = "系统凭据管理器不可用，密码以明文保存在本机"
			}
			ui.Text(c, msg).FontSize(12).TextColor(t.TextMuted).Grow(1).Wrap()
			dirty := m.ssid != m.saved || m.pass != m.storedPass
			if (ui.PrimaryButton(c, "保存").Disabled(!dirty || m.ssid == "").Clicked() || (submitted && dirty)) && m.ssid != "" {
				m.act.Save(m.ssid, m.pass)
			}
		})
	})
}

func (m *model) switches(c *ui.Context, t *ui.Theme) {
	card(c, t).Children(func() {
		sectionTitle(c, t, "设置", colSoyo)
		ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
			ui.Column(c).Grow(1).Gap(1).Cursor(ui.CursorPointer).OnClick(func() {
				m.act.SetAutostart(!m.autostart)
			}).Children(func() {
				ui.Text(c, "开机自启")
				ui.Text(c, "登录系统后在托盘待命（不会自动开热点）").FontSize(11.5).TextColor(t.TextMuted)
			})
			if ui.Switch(c, &m.autostart).Label("开机自启").Changed() {
				m.act.SetAutostart(m.autostart)
			}
		})
	})
}

// paper is the cream of 灯's notebook.
func paper(t *ui.Theme) (bg, rule ui.Color) {
	if t.Dark {
		return ui.Hex("#1A2230"), ui.RGBA(119, 187, 221, 0.10)
	}
	return ui.Hex("#FFFDF6"), ui.RGBA(51, 136, 187, 0.13)
}

const logLine = 19

func (m *model) logCard(c *ui.Context, t *ui.Theme) {
	card(c, t).Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Box(c).Size(8, 8).Radius(4).Background(colRana)
			ui.Text(c, "乐奈的吉他盒").FontSize(13).Bold().Grow(1)
			if m.act.OpenLogs != nil && ui.Button(c, "日志文件").FontSize(12).Clicked() {
				m.act.OpenLogs()
			}
		})
		bg, rule := paper(t)
		sc := ui.Scroll(c).Height(200).TrackScroll(&m.logScroll).Radius(8).Background(bg).Border(1, t.Border)
		if m.logScroll.Y >= m.logScroll.MaxY-1 {
			m.logScroll.Y = math.MaxFloat32 // stay at the newest line
		}
		sc.Children(func() {
			ui.Column(c).FillWidth().MinHeight(200).Padding(4, 10, 4, 22).
				Draw(func(p *ui.Painter, r ui.Rect) {
					for y := r.Y + 4 + logLine; y < r.Y+r.H; y += logLine {
						p.Line(r.X, y, r.X+r.W, y, 1, rule)
					}
					p.Line(r.X+14, r.Y, r.X+14, r.Y+r.H, 1, colAnon.Alpha(0.45))
				}).
				Children(func() {
					if len(m.logs) == 0 {
						ui.Text(c, "这里会记下每一次开关和检测……").FontSize(12).FixedLineHeight(logLine).TextColor(t.TextMuted)
					}
					for i, l := range m.logs {
						ui.Text(c, l).Key(i).FontSize(11.5).FixedLineHeight(logLine).Selectable().Wrap()
					}
				})
		})
	})
}

func (m *model) footer(c *ui.Context, t *ui.Theme) {
	ui.Row(c).Center().Gap(6).Padding(2, 0, 6, 0).Children(func() {
		for _, col := range members {
			ui.Box(c).Size(6, 6).Radius(3).Background(col)
		}
		ui.Text(c, "it's MyGO!!!!!  ·  v"+m.version).FontSize(11).TextColor(t.TextMuted)
	})
}
