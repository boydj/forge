package health

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeClock is a manual clock.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (f *fakeClock) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.t
}

func (f *fakeClock) Advance(d time.Duration) {
	f.mu.Lock()
	f.t = f.t.Add(d)
	f.mu.Unlock()
}

// recAnn records announcer verbs and can be told to fail some of them.
type recAnn struct {
	mu    sync.Mutex
	calls []string
	fail  map[string]error
}

// stateAnn is an announcer that also reports its current state (like the
// production forge-bgp-request script).
type stateAnn struct {
	recAnn
	state State
	err   error
}

func (s *stateAnn) State(context.Context) (State, error) { return s.state, s.err }

func (r *recAnn) do(verb string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.fail[verb]; err != nil {
		return err
	}
	r.calls = append(r.calls, verb)
	return nil
}

func (r *recAnn) Announce(context.Context) error { return r.do("announce") }
func (r *recAnn) Withdraw(context.Context) error { return r.do("withdraw") }
func (r *recAnn) Drain(context.Context) error    { return r.do("drain") }
func (r *recAnn) Undrain(context.Context) error  { return r.do("undrain") }

func (r *recAnn) setFail(verb string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fail == nil {
		r.fail = map[string]error{}
	}
	if err == nil {
		delete(r.fail, verb)
	} else {
		r.fail[verb] = err
	}
}

func (r *recAnn) take() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.calls
	r.calls = nil
	return out
}

// harness drives a controller with a fake clock and a switchable check.
type harness struct {
	t   *testing.T
	c   *Controller
	clk *fakeClock
	ann *recAnn
	ok  atomic.Bool
	cfg Config
}

func newHarness(t *testing.T, cfg Config) *harness {
	t.Helper()
	h := &harness{t: t, clk: &fakeClock{t: time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)}, ann: &recAnn{}, cfg: cfg.withDefaults()}
	h.ok.Store(true)
	chk := Check{Name: "probe", Run: func(context.Context) error {
		if h.ok.Load() {
			return nil
		}
		return errors.New("probe failed")
	}}
	h.c = New(cfg, []Check{chk}, h.ann).SetClock(h.clk.Now).SetLogger(quietLogger())
	return h
}

// ticks advances the clock by one interval and runs a cycle, n times.
func (h *harness) ticks(healthy bool, n int) {
	h.ok.Store(healthy)
	for i := 0; i < n; i++ {
		h.clk.Advance(h.cfg.Interval)
		h.c.Tick(context.Background())
	}
}

func (h *harness) wantState(s State) {
	h.t.Helper()
	if got := h.c.State(); got != s {
		h.t.Fatalf("state = %s, want %s (status %+v)", got, s, h.c.Status())
	}
}

func (h *harness) wantCalls(want ...string) {
	h.t.Helper()
	got := h.ann.take()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		h.t.Fatalf("announcer calls = %v, want %v", got, want)
	}
}

// bringUp takes a fresh harness from withdrawn to announced.
func (h *harness) bringUp() {
	h.t.Helper()
	// Cooldown since construction dominates K*interval with the defaults.
	need := int((h.cfg.Cooldown + h.cfg.Interval - 1) / h.cfg.Interval)
	if k := h.cfg.SuccessesToRecover; k > need {
		need = k
	}
	h.ticks(true, need)
	h.wantState(StateDrained)
	h.ticks(true, 1)
	h.wantState(StateAnnounced)
	h.ann.take()
}

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestBootAnnouncesOnlyAfterSuccessesAndCooldown(t *testing.T) {
	h := newHarness(t, Default())
	h.wantState(StateWithdrawn)
	if ok, detail := h.c.Healthy(); ok || detail != "starting" {
		t.Fatalf("Healthy before first cycle = %v %q", ok, detail)
	}
	// 6 successes (60 s) is not enough: the 120 s cooldown since start applies.
	h.ticks(true, 6)
	h.wantState(StateWithdrawn)
	h.wantCalls()
	h.ticks(true, 5) // t = 110 s
	h.wantState(StateWithdrawn)
	h.ticks(true, 1) // t = 120 s
	h.wantState(StateDrained)
	h.wantCalls("announce", "drain")
	if !h.c.Status().Reentering {
		t.Fatal("expected re-entry flag")
	}
	h.ticks(true, 1)
	h.wantState(StateAnnounced)
	h.wantCalls("undrain")
	if ok, detail := h.c.Healthy(); !ok || detail != "ok" {
		t.Fatalf("Healthy = %v %q", ok, detail)
	}
}

func TestBootNeverAnnouncesWhileUnhealthy(t *testing.T) {
	h := newHarness(t, Default())
	h.ticks(false, 100)
	h.wantState(StateWithdrawn)
	h.wantCalls()
	// A failure resets the success run: 5 good, 1 bad, 5 good -> still out.
	h.ticks(true, 5)
	h.ticks(false, 1)
	h.ticks(true, 5)
	h.wantState(StateWithdrawn)
	h.wantCalls()
	if ok, detail := h.c.Healthy(); !ok || detail != "ok" {
		t.Fatalf("Healthy = %v %q", ok, detail)
	}
}

func TestTransientFailureDoesNotTransition(t *testing.T) {
	h := newHarness(t, Default())
	h.bringUp()
	h.ticks(false, 1)
	h.ticks(true, 1)
	h.ticks(false, 2)
	h.ticks(true, 1)
	h.wantState(StateAnnounced)
	h.wantCalls()
	if ok, detail := h.c.Healthy(); !ok || detail != "ok" {
		t.Fatalf("Healthy = %v %q", ok, detail)
	}
}

func TestThreeFailuresDrain(t *testing.T) {
	h := newHarness(t, Default())
	h.bringUp()
	h.ticks(false, 2)
	h.wantState(StateAnnounced)
	h.wantCalls()
	h.ticks(false, 1)
	h.wantState(StateDrained)
	h.wantCalls("drain")
	if ok, detail := h.c.Healthy(); ok || !strings.Contains(detail, "probe: probe failed") {
		t.Fatalf("Healthy = %v %q", ok, detail)
	}
	st := h.c.Status()
	if st.Failures != 3 || st.Successes != 0 || st.Cooldown != 120*time.Second {
		t.Fatalf("status = %+v", st)
	}
}

func TestSixFailuresWithdraw(t *testing.T) {
	h := newHarness(t, Default())
	h.bringUp()
	h.ticks(false, 5)
	h.wantState(StateDrained)
	h.wantCalls("drain")
	h.ticks(false, 1)
	h.wantState(StateWithdrawn)
	h.wantCalls("withdraw")
	h.ticks(false, 20)
	h.wantCalls()
}

func TestRecoveryFromDrainedNeedsSuccessesAndCooldown(t *testing.T) {
	h := newHarness(t, Default())
	h.bringUp()
	h.ticks(false, 3)
	h.wantState(StateDrained) // t0
	h.ann.take()
	h.ticks(true, 6) // 60 s: K reached but cooldown not elapsed
	h.wantState(StateDrained)
	h.wantCalls()
	h.ticks(true, 5) // 110 s
	h.wantState(StateDrained)
	h.ticks(true, 1) // 120 s
	h.wantState(StateAnnounced)
	h.wantCalls("undrain")
}

func TestRecoveryFromWithdrawnGoesThroughDrained(t *testing.T) {
	h := newHarness(t, Default())
	h.bringUp()
	h.ticks(false, 6)
	h.wantState(StateWithdrawn)
	h.ann.take()
	h.ticks(true, 12) // 120 s since withdraw
	h.wantState(StateDrained)
	h.wantCalls("announce", "drain")
	// A failure during the drained re-entry tick cancels the fast path.
	h.ticks(false, 1)
	h.wantState(StateDrained)
	h.wantCalls()
	h.ticks(true, 5)
	h.wantState(StateDrained)
	h.ticks(true, 7) // K=6 and 120 s since the drained transition
	h.wantState(StateAnnounced)
	h.wantCalls("undrain")
}

func TestFlapBackoffDoubles(t *testing.T) {
	h := newHarness(t, Default())
	h.bringUp()
	flap := func(wantCooldown time.Duration) {
		t.Helper()
		h.ticks(false, 3)
		h.wantState(StateDrained)
		h.wantCalls("drain")
		if got := h.c.Status().Cooldown; got != wantCooldown {
			t.Fatalf("cooldown = %s, want %s", got, wantCooldown)
		}
		n := int(wantCooldown / h.cfg.Interval)
		h.ticks(true, n-1)
		h.wantState(StateDrained)
		h.ticks(true, 1)
		h.wantState(StateAnnounced)
		h.wantCalls("undrain")
	}
	flap(120 * time.Second)
	flap(240 * time.Second)
	flap(480 * time.Second)
	flap(900 * time.Second) // capped
	flap(900 * time.Second)
	// Quiet for longer than FlapWindow: back to the base cooldown.
	h.ticks(true, int(time.Hour/h.cfg.Interval)+1)
	flap(120 * time.Second)
}

func TestManualDrainOverridesChecks(t *testing.T) {
	h := newHarness(t, Default())
	h.bringUp()
	if err := h.c.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	h.wantState(StateDrained)
	h.wantCalls("drain")
	if !h.c.Status().Manual {
		t.Fatal("expected manual pin")
	}
	// Healthy for a long time: still drained.
	h.ticks(true, 200)
	h.wantState(StateDrained)
	h.wantCalls()
	// Failing for a long time: no withdraw either, the operator owns the state.
	h.ticks(false, 20)
	h.wantState(StateDrained)
	h.wantCalls()
	if ok, _ := h.c.Healthy(); ok {
		t.Fatal("Healthy should still reflect the checks")
	}
	// Second drain is a no-op.
	if err := h.c.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	h.wantCalls()
	// Undrain releases the pin; hysteresis applies from there.
	if err := h.c.Undrain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := h.c.Undrain(context.Background()); !errors.Is(err, ErrPinned) {
		t.Fatalf("second undrain err = %v", err)
	}
	h.ticks(true, 5)
	h.wantState(StateDrained)
	h.ticks(true, 1)
	h.wantState(StateAnnounced)
	h.wantCalls("undrain")
	if h.c.Status().Cooldown != 120*time.Second {
		t.Fatal("operator drain must not count as a flap")
	}
}

func TestManualWithdrawAndUndrainImmediateReentry(t *testing.T) {
	h := newHarness(t, Default())
	h.bringUp()
	if err := h.c.Withdraw(context.Background()); err != nil {
		t.Fatal(err)
	}
	h.wantState(StateWithdrawn)
	h.wantCalls("withdraw")
	h.ticks(true, 30) // healthy throughout maintenance, > cooldown
	h.wantCalls()
	if err := h.c.Undrain(context.Background()); err != nil {
		t.Fatal(err)
	}
	h.ticks(true, 1)
	h.wantState(StateDrained)
	h.wantCalls("announce", "drain")
	h.ticks(true, 1)
	h.wantState(StateAnnounced)
	h.wantCalls("undrain")
}

func TestAnnouncerFailureKeepsStateAndRetries(t *testing.T) {
	h := newHarness(t, Default())
	h.bringUp()
	h.ann.setFail("drain", errors.New("birdc: connection refused"))
	h.ticks(false, 3)
	h.wantState(StateAnnounced)
	h.wantCalls()
	h.ticks(false, 1)
	h.wantState(StateAnnounced)
	h.ann.setFail("drain", nil)
	h.ticks(false, 1) // 5 failures: drain succeeds now
	h.wantState(StateDrained)
	h.wantCalls("drain")
	// Drain keeps failing all the way to the withdraw threshold: withdraw
	// straight from announced.
	h2 := newHarness(t, Default())
	h2.bringUp()
	h2.ann.setFail("drain", errors.New("boom"))
	h2.ticks(false, 6)
	h2.wantState(StateWithdrawn)
	h2.wantCalls("withdraw")
}

func TestMetricsGauges(t *testing.T) {
	h := newHarness(t, Default())
	m := &Metrics{Healthy: newGauge(), Announced: newGauge()}
	h.c.SetMetrics(m)
	h.bringUp()
	if gaugeValue(t, m.Healthy) != 1 || gaugeValue(t, m.Announced) != 1 {
		t.Fatalf("gauges after bring-up: healthy=%v announced=%v", gaugeValue(t, m.Healthy), gaugeValue(t, m.Announced))
	}
	h.ticks(false, 3)
	if gaugeValue(t, m.Healthy) != 0 || gaugeValue(t, m.Announced) != 0 {
		t.Fatalf("gauges after drain: healthy=%v announced=%v", gaugeValue(t, m.Healthy), gaugeValue(t, m.Announced))
	}
	var nilM *Metrics
	nilM.SetHealthy(true) // must not panic
	(&Metrics{}).SetAnnounced(true)
}

func TestRunShutdownDrains(t *testing.T) {
	cfg := Default()
	cfg.Interval = 5 * time.Millisecond
	cfg.Cooldown = 0
	cfg.SuccessesToRecover = 1
	ann := &recAnn{}
	c := New(cfg, []Check{{Name: "ok", Run: func(context.Context) error { return nil }}}, ann).SetLogger(quietLogger())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	deadline := time.Now().Add(5 * time.Second)
	for c.State() != StateAnnounced {
		if time.Now().After(deadline) {
			t.Fatalf("never announced: %+v", c.Status())
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return")
	}
	if c.State() != StateDrained {
		t.Fatalf("state after shutdown = %s", c.State())
	}
	calls := ann.take()
	if calls[0] != "withdraw" {
		t.Fatalf("startup must reconcile with withdraw, got %v", calls)
	}
	if calls[len(calls)-1] != "drain" {
		t.Fatalf("shutdown must drain last, got %v", calls)
	}
	if strings.Join(calls, ",") != "withdraw,announce,drain,undrain,drain" {
		t.Fatalf("calls = %v", calls)
	}
}

func TestRunShutdownWhenNotAnnouncedDoesNothing(t *testing.T) {
	cfg := Default()
	cfg.Interval = 5 * time.Millisecond
	ann := &recAnn{}
	c := New(cfg, []Check{{Name: "bad", Run: func(context.Context) error { return errors.New("no") }}}, ann).SetLogger(quietLogger())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	time.Sleep(20 * time.Millisecond)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(ann.take(), ","); got != "withdraw" {
		t.Fatalf("calls = %s", got)
	}
}

func TestConfigDefaultsAndClamps(t *testing.T) {
	c := (Config{FailuresToDrain: 5, FailuresToWithdraw: 2, Cooldown: time.Hour, MaxBackoff: time.Minute}).withDefaults()
	if c.FailuresToWithdraw != 5 || c.MaxBackoff != time.Hour || c.Interval != 10*time.Second || c.CheckTimeout != 5*time.Second {
		t.Fatalf("%+v", c)
	}
	for _, s := range []State{StateWithdrawn, StateDrained, StateAnnounced, State(9)} {
		if s.String() == "" {
			t.Fatal("empty state name")
		}
	}
}

func TestStartupAdoptsAnnouncedState(t *testing.T) {
	h := newHarness(t, Config{})
	ann := &stateAnn{state: StateAnnounced}
	h.c = New(h.cfg, []Check{{Name: "probe", Run: func(context.Context) error { return nil }}}, ann).SetClock(h.clk.Now).SetLogger(quietLogger())
	h.c.reconcile(context.Background())
	if h.c.State() != StateAnnounced {
		t.Fatalf("state after adoption: %s", h.c.State())
	}
	if len(ann.calls) != 0 {
		t.Fatalf("adoption must not call the announcer, got %v", ann.calls)
	}
	// Healthy ticks keep it announced without any transition.
	for i := 0; i < 3; i++ {
		h.clk.Advance(h.cfg.Interval)
		h.c.Tick(context.Background())
	}
	if h.c.State() != StateAnnounced || len(ann.calls) != 0 {
		t.Fatalf("state %s calls %v", h.c.State(), ann.calls)
	}
}

func TestStartupWithdrawsWhenStateUnknown(t *testing.T) {
	h := newHarness(t, Config{})
	ann := &stateAnn{err: errors.New("no answer")}
	h.c = New(h.cfg, []Check{{Name: "probe", Run: func(context.Context) error { return nil }}}, ann).SetClock(h.clk.Now).SetLogger(quietLogger())
	h.c.reconcile(context.Background())
	if h.c.State() != StateWithdrawn || len(ann.calls) != 1 || ann.calls[0] != "withdraw" {
		t.Fatalf("state %s calls %v", h.c.State(), ann.calls)
	}
}

// TestNonCriticalFailureDoesNotWithdraw pins the criticality policy of
// ADR 0014: a failing non-critical check degrades the node (it shows on
// /status and the fleet page) but never takes the POP out of anycast,
// however long it fails. A critical check still drains as before.
func TestNonCriticalFailureDoesNotWithdraw(t *testing.T) {
	cfg := Default()
	var critFails, softFails atomic.Bool
	crit := Check{Name: "gemini", Run: func(context.Context) error {
		if critFails.Load() {
			return errors.New("listener down")
		}
		return nil
	}}
	soft := Check{Name: "replica", NonCritical: true, Run: func(context.Context) error {
		if softFails.Load() {
			return errors.New("lag 500 > 100 events")
		}
		return nil
	}}
	clk := &fakeClock{t: time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)}
	ann := &recAnn{}
	c := New(cfg, []Check{crit, soft}, ann).SetClock(clk.Now).SetLogger(quietLogger())
	tick := func(n int) {
		for i := 0; i < n; i++ {
			clk.Advance(cfg.Interval)
			c.Tick(context.Background())
		}
	}
	// Bring the node up.
	need := int((cfg.Cooldown + cfg.Interval - 1) / cfg.Interval)
	if k := cfg.SuccessesToRecover; k > need {
		need = k
	}
	tick(need + 1)
	if c.State() != StateAnnounced {
		t.Fatalf("state = %s, want announced", c.State())
	}
	ann.take()

	// The non-critical check fails for far longer than FailuresToWithdraw.
	softFails.Store(true)
	tick(cfg.FailuresToWithdraw * 3)
	if c.State() != StateAnnounced {
		t.Errorf("state = %s after sustained non-critical failure, want announced", c.State())
	}
	if calls := ann.take(); len(calls) != 0 {
		t.Errorf("announcer called %v for a non-critical failure", calls)
	}
	// It is still visible as degraded rather than hidden.
	ok, detail := c.Healthy()
	if !ok {
		t.Errorf("Healthy = false; a non-critical failure must not mark the node unhealthy")
	}
	if !strings.Contains(detail, "replica (non-critical)") {
		t.Errorf("detail %q does not report the degraded check", detail)
	}

	// A critical failure on top still drains on schedule.
	critFails.Store(true)
	tick(cfg.FailuresToDrain)
	if c.State() != StateDrained {
		t.Errorf("state = %s after critical failure, want drained", c.State())
	}
}
