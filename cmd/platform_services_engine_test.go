package cmd

import (
	"path/filepath"
	"testing"

	"github.com/Tnsor-Labs/brokoli/engine"
	"github.com/Tnsor-Labs/brokoli/extensions"
	"github.com/Tnsor-Labs/brokoli/store"
)

// An extension that needs the engine for anything recovery-related can only
// get it from a hook the recovering process actually calls. RegisterRoutes
// is not that hook: it comes from api.NewServer, and `--mode scheduler`
// starts api.NewMinimalServer instead while still running the sweep. So the
// scheduler process held none of the engine hooks an extension installed,
// and a run already executing on a remote worker was adopted and re-run
// in-cluster, both executions finishing.
//
// Testing the provider's own StartServices in isolation cannot catch that
// class: the defect is in which process calls it and with what. These tests
// are about the wiring.

type recordingPlatformServices struct {
	enabled    bool
	startCalls int
	gotStore   interface{}
	gotTail    []interface{}
	stopCalls  int
}

func (p *recordingPlatformServices) Enabled() bool                                        { return p.enabled }
func (p *recordingPlatformServices) RegisterRoutes(_, _, _ interface{}, _ ...interface{}) {}
func (p *recordingPlatformServices) StartServices(s interface{}, eng ...interface{}) {
	p.startCalls++
	p.gotStore = s
	p.gotTail = eng
}
func (p *recordingPlatformServices) StopServices()           { p.stopCalls++ }
func (p *recordingPlatformServices) MigrateDB(_ interface{}) {}

func platformWiringFixture(t *testing.T) (store.Store, *engine.Engine) {
	t.Helper()
	s, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "wiring.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, engine.NewEngine(s)
}

// The headline: every mode that runs the recovery sweep hands the engine
// over, and every mode that does not run it starts nothing at all.
func TestStartPlatformServicesGivesTheEngineToEveryRecoveringMode(t *testing.T) {
	cases := []struct {
		mode      string
		wantStart bool
	}{
		{"scheduler", true}, // the process that was missing the hooks
		{"all", true},       // must keep them; this is not a move
		{"api", false},
		{"worker", false},
		{"", false},
	}
	for _, c := range cases {
		t.Run(c.mode, func(t *testing.T) {
			s, eng := platformWiringFixture(t)
			p := &recordingPlatformServices{enabled: true}
			ext := &extensions.Registry{Platform: p}

			started := startPlatformServices(ext, c.mode, s, eng)

			if started != c.wantStart {
				t.Fatalf("started = %v, want %v", started, c.wantStart)
			}
			if !c.wantStart {
				if p.startCalls != 0 {
					t.Fatalf("StartServices called %d times in mode %q", p.startCalls, c.mode)
				}
				return
			}
			if p.startCalls != 1 {
				t.Fatalf("StartServices called %d times, want 1", p.startCalls)
			}
			if p.gotStore != store.Store(s) {
				t.Errorf("store not forwarded unchanged")
			}
			if len(p.gotTail) != 1 {
				t.Fatalf("variadic tail = %d values, want the engine", len(p.gotTail))
			}
			got, ok := p.gotTail[0].(*engine.Engine)
			if !ok {
				t.Fatalf("tail[0] is %T, want *engine.Engine", p.gotTail[0])
			}
			if got != eng {
				t.Errorf("a different engine was handed over than the one this process runs")
			}
		})
	}
}

// The gate's other reasons to refuse, so "scheduler starts it" cannot be
// achieved by starting it for everyone.
func TestStartPlatformServicesRefusesWhenThereIsNoPlatform(t *testing.T) {
	s, eng := platformWiringFixture(t)

	if startPlatformServices(nil, "scheduler", s, eng) {
		t.Error("started with a nil registry")
	}
	if startPlatformServices(&extensions.Registry{}, "scheduler", s, eng) {
		t.Error("started with no platform provider")
	}
	disabled := &recordingPlatformServices{enabled: false}
	if startPlatformServices(&extensions.Registry{Platform: disabled}, "scheduler", s, eng) {
		t.Error("started a disabled platform")
	}
	if disabled.startCalls != 0 {
		t.Errorf("disabled platform was started %d times", disabled.startCalls)
	}
}

// A nil engine is a legitimate state for a provider to receive, so the
// wiring must not start guarding against it and quietly skip the call.
func TestStartPlatformServicesStillStartsWithoutAnEngine(t *testing.T) {
	s, _ := platformWiringFixture(t)
	p := &recordingPlatformServices{enabled: true}

	if !startPlatformServices(&extensions.Registry{Platform: p}, "all", s, nil) {
		t.Fatal("platform services must still start when there is no engine")
	}
	if len(p.gotTail) != 1 {
		t.Fatalf("variadic tail = %d values, want one (a nil engine is still an argument)", len(p.gotTail))
	}
}
