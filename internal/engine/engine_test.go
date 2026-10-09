package engine

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestDecodePicksLastJSONLine(t *testing.T) {
	out := "noise\r\n{\"partial\":\r\n{\"op\":\"discover\",\"ok\":true,\"state\":\"On\",\"clients\":2}\r\n"
	r, err := decode(out)
	if err != nil {
		t.Fatal(err)
	}
	if r.str("op") != "discover" || !r.bl("ok") || r.str("state") != "On" || r.num("clients") != 2 {
		t.Fatalf("%+v", r)
	}
}

func TestDecodeSkipsBOM(t *testing.T) {
	r, err := decode("\ufeff{\"ok\":true}\n")
	if err != nil {
		t.Fatal(err)
	}
	if !r.bl("ok") {
		t.Fatalf("%+v", r)
	}
}

func TestDecodeNoAnswer(t *testing.T) {
	if _, err := decode("no json here"); !errors.Is(err, ErrNoAnswer) {
		t.Fatalf("err %v", err)
	}
}

func newTestEngine(runner runnerFunc) *Engine {
	e := &Engine{}
	e.run = runner
	return e
}

func TestDiscoverMapsFields(t *testing.T) {
	e := newTestEngine(func(ctx context.Context, envReq string) (string, error) {
		if !strings.Contains(envReq, `"op":"discover"`) {
			t.Fatalf("req %s", envReq)
		}
		return `{"op":"discover","ok":true,"state":"Off","ssid":"my-ap","clients":0,` +
			`"nic_name":"WLAN","nic_desc":"Test Wi-Fi","nic_status":"Up","uplink":"Ethernet"}` + "\n", nil
	})
	info, err := e.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.AdapterName != "WLAN" || info.AdapterDesc != "Test Wi-Fi" || info.AdapterStatus != "Up" ||
		info.Uplink != "Ethernet" || info.State != "Off" || info.SSID != "my-ap" || info.Clients != 0 {
		t.Fatalf("%+v", info)
	}
}

func TestConfigureFailsWithoutOK(t *testing.T) {
	e := newTestEngine(func(ctx context.Context, envReq string) (string, error) {
		return `{"op":"configure","ok":false,"error":"ssid readback mismatch after configure"}` + "\n", nil
	})
	err := e.Configure(context.Background(), "my-ap", "secret")
	if err == nil || !strings.Contains(err.Error(), "ssid readback") {
		t.Fatalf("err %v", err)
	}
}

func TestConfigureSendsCredentialsInEnv(t *testing.T) {
	var got string
	e := newTestEngine(func(ctx context.Context, envReq string) (string, error) {
		got = envReq
		return `{"op":"configure","ok":true}` + "\n", nil
	})
	if err := e.Configure(context.Background(), "my-ap", "secret"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, `"passkey":"secret"`) || !strings.Contains(got, `"ssid":"my-ap"`) {
		t.Fatalf("req %s", got)
	}
}

func TestStartTryMapsVerdict(t *testing.T) {
	e := newTestEngine(func(ctx context.Context, envReq string) (string, error) {
		return `{"op":"start-try","ok":true,"state":"Off","fresh":"On","wfd":true,"broadcast":false}` + "\n", nil
	})
	v, err := e.StartTry(context.Background(), 45, "my-ap")
	if err != nil {
		t.Fatal(err)
	}
	if !v.OK || v.Already || v.State != "Off" || v.Fresh != "On" || !v.WFD || v.Broadcast {
		t.Fatalf("%+v", v)
	}
}

func TestStopWaitMapsVerdict(t *testing.T) {
	e := newTestEngine(func(ctx context.Context, envReq string) (string, error) {
		return `{"op":"stop-wait","ok":true,"nothing":true,"state":"Off"}` + "\n", nil
	})
	v, err := e.StopWait(context.Background(), 20)
	if err != nil {
		t.Fatal(err)
	}
	if !v.OK || !v.Nothing || v.State != "Off" {
		t.Fatalf("%+v", v)
	}
}

func TestRunPropagatesProcessError(t *testing.T) {
	e := newTestEngine(func(ctx context.Context, envReq string) (string, error) {
		return "", errors.New("powershell exploded")
	})
	if _, err := e.Discover(context.Background()); err == nil || !strings.Contains(err.Error(), "powershell exploded") {
		t.Fatalf("err %v", err)
	}
}
