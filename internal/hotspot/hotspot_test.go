package hotspot

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// --- fakes ---

type fakeEngine struct {
	info      Info
	configure []([2]string)
	startTry  int
	stopWait  int

	configureErr error
	startVerdict Verdict
	startErr     error
	stopVerdict  Verdict
	stopErr      error
}

func (f *fakeEngine) Discover(ctx context.Context) (Info, error) { return f.info, nil }
func (f *fakeEngine) Configure(ctx context.Context, ssid, passkey string) error {
	f.configure = append(f.configure, [2]string{ssid, passkey})
	return f.configureErr
}
func (f *fakeEngine) StartTry(ctx context.Context, seconds int, ssid string) (Verdict, error) {
	f.startTry++
	return f.startVerdict, f.startErr
}
func (f *fakeEngine) StopWait(ctx context.Context, seconds int) (Verdict, error) {
	f.stopWait++
	return f.stopVerdict, f.stopErr
}

type fakeDeps struct {
	engine   *fakeEngine
	tun      Tun
	tunErr   error
	setCalls []bool
	setErr   error
	inetOK   bool
}

func (f *fakeDeps) clashTun(ctx context.Context) (Tun, error) { return f.tun, f.tunErr }
func (f *fakeDeps) clashSet(ctx context.Context, on bool) error {
	f.setCalls = append(f.setCalls, on)
	return f.setErr
}
func (f *fakeDeps) inet(ctx context.Context, retries, gapSec int) bool { return f.inetOK }

func newManager(d *fakeDeps) (*Manager, *[]string, *[]State) {
	logs := &[]string{}
	states := &[]State{}
	e := d.engine
	if e == nil {
		e = &fakeEngine{}
		d.engine = e
	}
	m := New(Deps{
		Engine:   e,
		ClashTun: d.clashTun,
		ClashSet: d.clashSet,
		Inet:     d.inet,
		Broadcast: func(iface, ssid string) bool {
			return false
		},
		Sleep: func(ctx context.Context, _ time.Duration) error { return nil },
		Log:   func(s string) { *logs = append(*logs, s) },
		OnChange: func(s State) {
			*states = append(*states, s)
		},
		Now: func() time.Time { return time.Unix(0, 0) },
	})
	return m, logs, states
}

// eng fetches the fake engine installed on the manager.
func eng(m *Manager) *fakeEngine { return m.Engine.(*fakeEngine) }

func upInfo() Info {
	return Info{AdapterName: "WLAN", AdapterDesc: "Test Wi-Fi 6E", AdapterStatus: "Up", Uplink: "Ethernet", State: "Off", SSID: "old-ssid"}
}

// --- start flow ---

func TestStartSuccessTurnsTunOffAndKeepsItOff(t *testing.T) {
	d := &fakeDeps{tun: TunOn, inetOK: true}
	m, logs, _ := newManager(d)
	eng(m).info = upInfo()
	eng(m).startVerdict = Verdict{OK: true, State: "On"}

	m.start(context.Background(), "my-ap", "secret")

	if len(d.setCalls) != 1 || d.setCalls[0] != false {
		t.Fatalf("setCalls %v", d.setCalls)
	}
	if len(eng(m).configure) != 1 || eng(m).configure[0] != [2]string{"my-ap", "secret"} {
		t.Fatalf("configure %v", eng(m).configure)
	}
	if eng(m).startTry != 1 {
		t.Fatalf("startTry %d", eng(m).startTry)
	}
	st := m.State()
	if st.Phase != On {
		t.Fatalf("phase %v", st.Phase)
	}
	if st.Tun != TunOff || st.TunTurnedOff {
		t.Fatalf("tun %v turnedOff %v", st.Tun, st.TunTurnedOff)
	}
	assertLogged(t, *logs, "STEP1", "STEP2", "STEP3 结果: state=On OK", "STEP5", "FINAL: 热点开启")
}

func TestStartAlreadyOnTouchesNothing(t *testing.T) {
	d := &fakeDeps{tun: TunOn, inetOK: true}
	m, logs, _ := newManager(d)
	info := upInfo()
	info.State = "On"
	eng(m).info = info

	m.start(context.Background(), "my-ap", "secret")

	if len(d.setCalls) != 0 || eng(m).startTry != 0 || len(eng(m).configure) != 0 {
		t.Fatalf("side effects: setCalls %v configure %v startTry %d", d.setCalls, eng(m).configure, eng(m).startTry)
	}
	if m.State().Phase != On {
		t.Fatalf("phase %v", m.State().Phase)
	}
	assertLogged(t, *logs, "热点已在运行")
}

func TestStartTunDisableFailAbortsAndDoesNotRestore(t *testing.T) {
	d := &fakeDeps{tun: TunOn, setErr: errors.New("api down")}
	m, _, _ := newManager(d)
	eng(m).info = upInfo()

	m.start(context.Background(), "my-ap", "secret")

	// Only the failed disable attempt; no restore (nothing was changed).
	if len(d.setCalls) != 1 || d.setCalls[0] != false {
		t.Fatalf("setCalls %v", d.setCalls)
	}
	if eng(m).startTry != 0 || eng(m).stopWait != 0 {
		t.Fatalf("startTry %d stopWait %d", eng(m).startTry, eng(m).stopWait)
	}
	st := m.State()
	if st.Phase != Idle || st.LastError == "" {
		t.Fatalf("phase %v err %q", st.Phase, st.LastError)
	}
}

func TestStartInetDeadAfterTunOffRevertsTun(t *testing.T) {
	d := &fakeDeps{tun: TunOn, inetOK: false}
	m, logs, _ := newManager(d)
	eng(m).info = upInfo()

	m.start(context.Background(), "my-ap", "secret")

	if len(d.setCalls) != 2 || d.setCalls[0] != false || d.setCalls[1] != true {
		t.Fatalf("setCalls %v", d.setCalls)
	}
	if eng(m).startTry != 0 || eng(m).stopWait != 0 {
		t.Fatalf("startTry %d stopWait %d", eng(m).startTry, eng(m).stopWait)
	}
	assertLogged(t, *logs, "REVERT")
	if m.State().Phase != Idle {
		t.Fatalf("phase %v", m.State().Phase)
	}
}

func TestStartFailureRollsBackAndRecoversTun(t *testing.T) {
	d := &fakeDeps{tun: TunOn, inetOK: true}
	m, logs, _ := newManager(d)
	eng(m).info = upInfo()
	eng(m).startVerdict = Verdict{OK: false, State: "Off", Fresh: "Off"}

	m.start(context.Background(), "my-ap", "secret")

	if eng(m).startTry != 1 || eng(m).stopWait != 1 {
		t.Fatalf("startTry %d stopWait %d", eng(m).startTry, eng(m).stopWait)
	}
	// TUN off in STEP1, restored in the recovery.
	if len(d.setCalls) != 2 || d.setCalls[0] != false || d.setCalls[1] != true {
		t.Fatalf("setCalls %v", d.setCalls)
	}
	assertLogged(t, *logs, "STEP3 回滚", "RECOVERY")
	st := m.State()
	if st.Phase != Idle || st.LastError == "" {
		t.Fatalf("phase %v err %q", st.Phase, st.LastError)
	}
	if st.Tun != TunOn {
		t.Fatalf("tun %v", st.Tun)
	}
}

func TestStartMissingCredentials(t *testing.T) {
	d := &fakeDeps{tun: TunOff}
	m, logs, _ := newManager(d)
	eng(m).info = upInfo()

	m.start(context.Background(), "", "")

	if eng(m).startTry != 0 {
		t.Fatal("startTry ran without credentials")
	}
	assertLogged(t, *logs, "请先在设置里保存")
}

// --- stop flow ---

func TestStopTurnsHotspotOff(t *testing.T) {
	d := &fakeDeps{tun: TunOn}
	m, logs, _ := newManager(d)
	info := upInfo()
	info.State = "On"
	eng(m).info = info
	eng(m).stopVerdict = Verdict{OK: true, State: "Off"}

	m.stop(context.Background())

	if eng(m).stopWait != 1 {
		t.Fatalf("stopWait %d", eng(m).stopWait)
	}
	if m.State().Phase != Off {
		t.Fatalf("phase %v", m.State().Phase)
	}
	// Stop never touches TUN; the user re-enables it in Clash.
	if len(d.setCalls) != 0 {
		t.Fatalf("setCalls %v", d.setCalls)
	}
	assertLogged(t, *logs, "state=Off OK", "手动开回 TUN")
}

func TestStopWhenNotRunning(t *testing.T) {
	d := &fakeDeps{}
	m, logs, _ := newManager(d)
	eng(m).info = upInfo() // Off

	m.stop(context.Background())

	if eng(m).stopWait != 0 {
		t.Fatalf("stopWait %d", eng(m).stopWait)
	}
	assertLogged(t, *logs, "无需关闭")
}

// --- refresh ---

func TestRefreshAppliesDiscovery(t *testing.T) {
	d := &fakeDeps{tun: TunOn}
	m, _, _ := newManager(d)
	eng(m).info = upInfo()

	m.refresh(context.Background(), true)

	st := m.State()
	if st.Phase != Off || !st.HasAP || st.AdapterName != "WLAN" || st.Uplink != "Ethernet" || st.Tun != TunOn {
		t.Fatalf("state %+v", st)
	}
}

func TestRefreshDiscoveryFailure(t *testing.T) {
	// The fake engine cannot fail Discover; swap in a failing one.
	m, _, _ := newManager(&fakeDeps{})
	m.Engine = &failingEngine{}

	m.refresh(context.Background(), true)

	st := m.State()
	if st.Phase != Idle || st.LastError == "" {
		t.Fatalf("phase %v err %q", st.Phase, st.LastError)
	}
}

type failingEngine struct{ fakeEngine }

func (failingEngine) Discover(ctx context.Context) (Info, error) {
	return Info{}, errors.New("boom")
}

func assertLogged(t *testing.T, logs []string, want ...string) {
	t.Helper()
	joined := strings.Join(logs, "\n")
	for _, w := range want {
		if !strings.Contains(joined, w) {
			t.Fatalf("log missing %q in:\n%s", w, joined)
		}
	}
}

// compile-time check the fakes implement the interface
var _ Engine = (*fakeEngine)(nil)

func ExamplePhase_String() {
	fmt.Println(On, Starting)
	// Output: on starting
}
