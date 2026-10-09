// Package hotspot coordinates a full hotspot run: it turns Clash TUN off
// first (the two are mutually exclusive), configures and starts the
// Windows Mobile Hotspot over the engine, verifies with live signals,
// and rolls every step back on failure. It mirrors hotspot.ps1 of the
// PowerShell version, step for step.
package hotspot

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/xiaomai1011/GZIST-Hotspot/internal/engine"
)

// Phase is the coarse state shown to the user.
type Phase int

const (
	Idle     Phase = iota // unknown; refresh to find out
	Checking              // a refresh is running
	Off
	On
	Starting
	Stopping
)

func (p Phase) String() string {
	switch p {
	case Checking:
		return "checking"
	case Off:
		return "off"
	case On:
		return "on"
	case Starting:
		return "starting"
	case Stopping:
		return "stopping"
	default:
		return "idle"
	}
}

// Busy reports whether a start or stop is running.
func (p Phase) Busy() bool { return p == Starting || p == Stopping }

// Tun mirrors the Clash TUN switch as far as we can see it.
type Tun int

const (
	TunUnknown Tun = iota
	TunOff
	TunOn
)

// State is a snapshot for the UI.
type State struct {
	Phase        Phase
	Adapter      string // "Name [Desc] (Status)"
	AdapterName  string
	Uplink       string
	SSID         string // configured on the access point
	Clients      int
	HasAP        bool // discovery succeeded at least once
	Tun          Tun
	TunTurnedOff bool // this app turned TUN off for the current hotspot run
	InetOK       bool
	InetKnown    bool
	LastError    string
	LastAction   time.Time
}

// Engine is what the manager needs from the WinRT bridge.
type Engine interface {
	Discover(ctx context.Context) (Info, error)
	Configure(ctx context.Context, ssid, passkey string) error
	StartTry(ctx context.Context, seconds int, ssid string) (Verdict, error)
	StopWait(ctx context.Context, seconds int) (Verdict, error)
}

// Info and Verdict shadow the engine types so callers need no import.
type Info = engine.Info
type Verdict = engine.Verdict

type cmdKind int

const (
	cmdRefresh cmdKind = iota
	cmdStart
	cmdStop
)

type cmd struct {
	kind    cmdKind
	quiet   bool
	ssid    string
	passkey string
	done    chan struct{}
}

// Deps wires the outside world; tests replace every field.
type Deps struct {
	Engine    Engine
	ClashTun  func(ctx context.Context) (Tun, error)
	ClashSet  func(ctx context.Context, on bool) error
	Inet      func(ctx context.Context, retries, gapSec int) bool
	Broadcast func(iface, ssid string) bool
	Sleep     func(ctx context.Context, d time.Duration) error
	Log       func(string)
	Notify    func(title, body string)
	OnChange  func(State)
	Now       func() time.Time
	// RefreshEvery is the idle poll period; zero means 30s.
	RefreshEvery time.Duration
}

// Manager runs the state machine; the other methods only queue work.
type Manager struct {
	Deps

	mu    sync.Mutex
	state State
	cmds  chan cmd
}

// New returns a manager.
func New(d Deps) *Manager {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Sleep == nil {
		d.Sleep = sleep
	}
	if d.RefreshEvery == 0 {
		d.RefreshEvery = 30 * time.Second
	}
	return &Manager{Deps: d, cmds: make(chan cmd, 8)}
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// State returns the current snapshot.
func (m *Manager) State() State {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state
}

func (m *Manager) update(fn func(*State)) {
	m.mu.Lock()
	fn(&m.state)
	s := m.state
	m.mu.Unlock()
	if m.OnChange != nil {
		m.OnChange(s)
	}
}

func (m *Manager) log(s string) {
	if m.Log != nil {
		m.Log(s)
	}
}

func (m *Manager) send(c cmd) {
	select {
	case m.cmds <- c:
	default: // queue full: a pending op will cover it
		if c.done != nil {
			close(c.done)
		}
	}
}

// Refresh checks the hotspot state now. Manual refreshes log what they
// find, like the PowerShell status run did.
func (m *Manager) Refresh() { m.send(cmd{kind: cmdRefresh}) }

// Start turns the hotspot on with the given credentials.
func (m *Manager) Start(ssid, passkey string) {
	m.send(cmd{kind: cmdStart, ssid: ssid, passkey: passkey})
}

// Stop turns the hotspot off.
func (m *Manager) Stop() { m.send(cmd{kind: cmdStop}) }

// Run processes commands and refreshes every RefreshEvery until ctx ends.
func (m *Manager) Run(ctx context.Context) {
	m.refresh(ctx, false)
	t := time.NewTimer(m.RefreshEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if !m.State().Phase.Busy() {
				m.refresh(ctx, true)
			}
		case c := <-m.cmds:
			m.handle(ctx, c)
		}
		if !t.Stop() {
			select {
			case <-t.C:
			default:
			}
		}
		t.Reset(m.RefreshEvery)
	}
}

func (m *Manager) handle(ctx context.Context, c cmd) {
	switch c.kind {
	case cmdRefresh:
		m.refresh(ctx, c.quiet)
	case cmdStart:
		m.start(ctx, c.ssid, c.passkey)
	case cmdStop:
		m.stop(ctx)
	}
	if c.done != nil {
		close(c.done)
	}
}

// refresh re-reads the world. quiet refreshes (the idle poll) log
// nothing and skip the internet probe.
func (m *Manager) refresh(ctx context.Context, quiet bool) {
	m.update(func(s *State) { s.Phase = Checking })
	info, err := m.Engine.Discover(ctx)
	if err != nil {
		m.update(func(s *State) {
			s.Phase, s.LastError = Idle, "检测热点状态失败: " + err.Error()
		})
		if !quiet {
			m.log("检测失败: " + err.Error())
		}
		return
	}
	on := info.State == "On"
	m.update(func(s *State) {
		s.Adapter = fmt.Sprintf("%s [%s] (%s)", info.AdapterName, info.AdapterDesc, info.AdapterStatus)
		s.AdapterName = info.AdapterName
		s.Uplink = info.Uplink
		s.SSID = info.SSID
		s.Clients = info.Clients
		s.HasAP = true
		s.LastError = ""
		if !s.Phase.Busy() {
			if on {
				s.Phase = On
			} else {
				s.Phase = Off
			}
		}
	})
	if t, err := m.ClashTun(ctx); err != nil {
		m.update(func(s *State) { s.Tun = TunUnknown })
		if !quiet {
			m.log("clash TUN: unknown (api fail)")
		}
	} else {
		m.update(func(s *State) { s.Tun = t })
	}
	if !quiet {
		m.logf("adapter: %s", m.State().Adapter)
		m.logf("uplink profile: %s", info.Uplink)
		m.logf("hotspot: state=%s ssid=[%s] clients=%d", info.State, info.SSID, info.Clients)
		m.logf("clash TUN: %s", m.State().Tun)
		ok := m.Inet(ctx, 3, 3)
		m.update(func(s *State) { s.InetOK, s.InetKnown = ok, true })
		m.logf("inet check: %s", okOK(ok))
	}
}

func okOK(b bool) string {
	if b {
		return "OK"
	}
	return "FAIL"
}

func tunWord(t Tun) string {
	switch t {
	case TunOn:
		return "enabled"
	case TunOff:
		return "disabled"
	default:
		return "unknown (api fail)"
	}
}

// String makes %s format verbs print the tunWord form.
func (t Tun) String() string { return tunWord(t) }

func (m *Manager) logf(format string, args ...any) {
	m.log(fmt.Sprintf(format, args...))
}

// start mirrors hotspot.ps1 -Action start, STEP1 through STEP5.
func (m *Manager) start(ctx context.Context, ssid, passkey string) {
	m.update(func(s *State) { s.Phase, s.LastError = Starting, "" })
	m.log("开始开启热点（全程约 1~2 分钟）…")
	if ssid == "" || passkey == "" {
		m.abort(ctx, false, "请先在设置里保存热点名称和密码")
		return
	}

	// --- STEP 0: see the world ---
	info, err := m.Engine.Discover(ctx)
	if err != nil {
		m.abort(ctx, false, err.Error())
		return
	}
	m.update(func(s *State) {
		s.Adapter = fmt.Sprintf("%s [%s] (%s)", info.AdapterName, info.AdapterDesc, info.AdapterStatus)
		s.AdapterName = info.AdapterName
		s.Uplink = info.Uplink
		s.SSID = info.SSID
		s.HasAP = true
	})
	m.logf("adapter: %s", m.State().Adapter)
	m.logf("uplink profile: %s", info.Uplink)
	if info.State == "On" {
		m.log("热点已在运行 —— 跳过开启")
		m.log("FINAL: hotspot ON + TUN untouched")
		m.finish(On, "")
		return
	}

	tun, terr := m.ClashTun(ctx)
	if terr != nil {
		tun = TunUnknown
	}
	m.update(func(s *State) { s.Tun = tun })
	m.logf("clash TUN: %s", tunWord(tun))

	// --- STEP 1: TUN off, or the hotspot kills the network ---
	tunTurnedOff := false
	switch tun {
	case TunOn:
		m.log("STEP1 关闭 Clash TUN …")
		if err := m.ClashSet(ctx, false); err != nil {
			m.abort(ctx, false, "无法经 mihomo API 关闭 TUN: "+err.Error())
			return
		}
		tunTurnedOff = true
		m.update(func(s *State) { s.Tun, s.TunTurnedOff = TunOff, true })
		if err := m.Sleep(ctx, 6*time.Second); err != nil {
			m.abort(ctx, tunTurnedOff, "等待时被取消")
			return
		}
		if !m.Inet(ctx, 3, 4) {
			m.log("REVERT 关 TUN 后直连失效 —— 恢复 TUN …")
			if err := m.ClashSet(ctx, true); err == nil {
				m.update(func(s *State) { s.Tun, s.TunTurnedOff = TunOn, false })
			}
			m.abort(ctx, false, "关闭 TUN 后直连不可用，已恢复 TUN 并中止（热点未开启）")
			return
		}
		m.log("STEP1 完成：TUN 已关，直连正常")
	case TunUnknown:
		m.log("STEP1 跳过：Clash TUN 状态未知（API 不可达），按已关闭继续")
	default:
		m.log("STEP1 跳过：TUN 未开启")
	}

	// --- STEP 2: write the SSID/passkey ---
	m.logf("STEP2 配置热点 ssid=[%s] …", ssid)
	if err := m.Engine.Configure(ctx, ssid, passkey); err != nil {
		m.abort(ctx, tunTurnedOff, "配置热点失败: "+err.Error())
		return
	}
	m.log("STEP2 完成：SSID/密码已写入")

	// --- STEP 3: start tethering, cross-check live signals ---
	m.log("STEP3 启动移动热点（最长等待 45 秒）…")
	v, err := m.Engine.StartTry(ctx, 45, ssid)
	if err != nil || !v.OK {
		if err != nil {
			m.log("STEP3 失败: " + err.Error())
		} else {
			m.logf("STEP3 失败: state=%s fresh=%s wfd=%v broadcast=%v（三重信号均未命中）",
				v.State, v.Fresh, v.WFD, v.Broadcast)
		}
		m.log("STEP3 回滚：停止热点以清理半开状态 …")
		if sv, serr := m.Engine.StopWait(ctx, 20); serr != nil {
			m.log("回滚停止失败: " + serr.Error())
		} else if sv.Nothing {
			m.log("回滚：热点本就未在运行")
		} else {
			m.logf("回滚完成: state=%s", sv.State)
		}
		m.abort(ctx, tunTurnedOff, "启动热点失败：45 秒内未进入 On 且实时信号均未命中")
		return
	}
	switch {
	case v.Already:
		m.log("STEP3 结果: 热点已在运行")
	case v.State == "On":
		m.log("STEP3 结果: state=On OK")
	default:
		m.logf("STEP3 判定: 热点实际在运行（状态属性滞后: %s, fresh=%s, wfd=%v, broadcast=%v）",
			v.State, v.Fresh, v.WFD, v.Broadcast)
	}

	// --- STEP 4: verify broadcast + internet (TUN still off) ---
	if err := m.Sleep(ctx, 3*time.Second); err != nil {
		m.finish(On, "")
		return
	}
	bc := false
	if m.Broadcast != nil {
		bc = m.Broadcast(m.State().AdapterName, ssid)
	}
	m.logf("STEP4 SSID 广播可见: %v", bc)
	ok := m.Inet(ctx, 3, 3)
	m.update(func(s *State) { s.InetOK, s.InetKnown = ok, true })
	m.logf("STEP4 直连联网检测（TUN 关）: %s", okOK(ok))

	// --- STEP 5: TUN stays OFF while the hotspot is ON ---
	m.update(func(s *State) { s.TunTurnedOff = false })
	m.log("STEP5 TUN 保持关闭（TUN 与热点互斥，绝不自动恢复）")
	if !ok {
		m.log("FINAL: 热点已开启，但直连检测失败 —— 检查上行网络后刷新重试")
	} else {
		m.log("FINAL: 热点开启 + TUN 关闭 + 直连正常")
	}
	m.log("HINT: 需要代理的客户端请各自配置；主机的代理请用 Clash 系统代理模式，关热点后再开回 TUN")
	m.finish(On, "")
	if m.Notify != nil {
		m.Notify("热点已开启", fmt.Sprintf("SSID %s —— TUN 保持关闭，需要代理请用系统代理模式", ssid))
	}
}

// stop mirrors hotspot.ps1 -Action stop.
func (m *Manager) stop(ctx context.Context) {
	m.update(func(s *State) { s.Phase, s.LastError = Stopping, "" })
	m.log("关闭热点 …")
	info, err := m.Engine.Discover(ctx)
	if err != nil {
		m.abort(ctx, false, err.Error())
		return
	}
	if info.State != "On" {
		m.log("热点未在运行 —— 无需关闭")
		m.finish(Off, "")
		return
	}
	v, err := m.Engine.StopWait(ctx, 20)
	if err != nil {
		m.log("关闭失败: " + err.Error())
		m.update(func(s *State) { s.Phase, s.LastError = Idle, "关闭热点失败: " + err.Error() })
		return
	}
	if v.Nothing {
		m.log("关闭结果: 热点本就未在运行")
	} else if v.State == "Off" {
		m.log("关闭结果: state=Off OK")
	} else {
		m.logf("关闭结果: state=%s（状态机已离开 On）", v.State)
	}
	m.log("HINT: 需要代理时请在 Clash 界面手动开回 TUN")
	switch v.State {
	case "Off":
		m.finish(Off, "")
	case "On":
		m.update(func(s *State) { s.Phase, s.LastError = On, "关闭超时：热点仍在运行" })
	default:
		// Unknown or odd states mean the hotspot is not actually running;
		// the stale On readout that led here settles late. Show "off",
		// not a mystery state.
		m.finish(Off, "")
	}
	if v.State == "Off" && m.Notify != nil {
		m.Notify("热点已关闭", "如需代理请记得在 Clash 开回 TUN")
	}
}

// finish settles into a stable phase.
func (m *Manager) finish(p Phase, notice string) {
	m.update(func(s *State) {
		s.Phase, s.LastError, s.LastAction = p, notice, m.Now()
	})
}

// abort records an error, restores TUN if this run turned it off, and
// leaves the phase unknown (a refresh follows).
func (m *Manager) abort(ctx context.Context, tunTurnedOff bool, msg string) {
	if tunTurnedOff || m.State().TunTurnedOff {
		m.log("RECOVERY 本次运行关闭过 TUN —— 正在恢复 …")
		if err := m.ClashSet(ctx, true); err != nil {
			m.log("RECOVERY 失败：TUN API 出错 —— 请在 Clash GUI 手动开启 TUN")
		} else {
			m.update(func(s *State) { s.Tun = TunOn })
			if err := m.Sleep(ctx, 5*time.Second); err == nil && m.Inet(ctx, 3, 4) {
				m.log("RECOVERY 完成：TUN 开 + 联网正常")
			} else {
				m.log("RECOVERY 警告：TUN 已开但联网检测失败 —— 请检查 Clash GUI")
			}
		}
	}
	m.update(func(s *State) {
		s.TunTurnedOff = false
		s.LastError = msg
		s.Phase = Idle
		s.LastAction = m.Now()
	})
	m.log(msg)
	m.log("hint: 点「刷新」查看状态，或重试")
	if m.Notify != nil {
		m.Notify("热点操作失败", msg)
	}
}

// ErrNoEngine guards against a nil Engine sneaking in.
var ErrNoEngine = errors.New("engine 未设置")
