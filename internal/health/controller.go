package health

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// Controller runs check cycles and moves the announcement state with
// hysteresis. Create it with New, then call Run (or Tick from tests).
//
// Transitions (health-driven, unless pinned by an operator):
//
//	withdrawn -> drained    K successes and cooldown elapsed   announce + drain
//	drained   -> announced  next healthy tick (re-entry)       undrain
//	                        or K successes and cooldown        undrain
//	announced -> drained    N consecutive failures             drain
//	drained   -> withdrawn  M consecutive failures (M >= N)    withdraw
//	announced -> withdrawn  M consecutive failures             withdraw
//	                        (only when drain kept failing)
type Controller struct {
	cfg  Config
	chk  *Checker
	ann  Announcer
	log  *slog.Logger
	met  *Metrics
	now  func() time.Time
	base time.Duration // configured cooldown

	// opMu serialises evaluation and transitions (which may block on the
	// announcer for up to its timeout); mu guards the snapshot fields.
	opMu sync.Mutex
	mu   sync.RWMutex

	state      State
	since      time.Time // last state change
	manual     bool      // operator pin: checks do not move the state
	reentering bool      // in drained on the way back from withdrawn
	failures   int       // consecutive failed cycles
	successes  int       // consecutive healthy cycles
	cycles     int64
	cooldown   time.Duration // effective cooldown (base, doubled by flaps)
	lastDrain  time.Time     // last health-driven exit from announced
	lastCycle  time.Time
	results    []Result
	healthy    bool
}

// New builds a controller. ann may be nil (NopAnnouncer).
func New(cfg Config, checks []Check, ann Announcer) *Controller {
	cfg = cfg.withDefaults()
	if ann == nil {
		ann = NopAnnouncer{}
	}
	c := &Controller{
		cfg:      cfg,
		chk:      &Checker{Checks: checks, Timeout: cfg.CheckTimeout},
		ann:      ann,
		log:      slog.Default(),
		now:      time.Now,
		base:     cfg.Cooldown,
		state:    StateWithdrawn,
		cooldown: cfg.Cooldown,
	}
	c.since = c.now()
	return c
}

// SetLogger sets the structured logger (default slog.Default()).
func (c *Controller) SetLogger(l *slog.Logger) *Controller {
	c.log = loggerOr(l).With("component", "health")
	return c
}

// SetMetrics sets the gauge sink; nil is allowed.
func (c *Controller) SetMetrics(m *Metrics) *Controller {
	c.met = m
	return c
}

// SetClock injects a clock (tests). Must be called before Run.
func (c *Controller) SetClock(now func() time.Time) *Controller {
	c.now = now
	c.mu.Lock()
	c.since = now()
	c.mu.Unlock()
	return c
}

// Status is a snapshot of the controller.
type Status struct {
	State State
	// Manual is true while an operator pin (Drain/Withdraw) is in effect.
	Manual bool
	// Healthy is the outcome of the last cycle; Detail lists failing checks.
	Healthy bool
	Detail  string
	Results []Result
	// Failures and Successes are the consecutive-cycle counters.
	Failures  int
	Successes int
	Cycles    int64
	// Since is the time of the last state change.
	Since time.Time
	// LastCycle is when the last cycle finished (zero before the first).
	LastCycle time.Time
	// Cooldown is the effective cooldown currently applied to recovery.
	Cooldown time.Duration
	// Reentering is true while drained on the way back from withdrawn.
	Reentering bool
}

// Status returns a snapshot.
func (c *Controller) Status() Status {
	c.mu.RLock()
	defer c.mu.RUnlock()
	rs := make([]Result, len(c.results))
	copy(rs, c.results)
	return Status{
		State: c.state, Manual: c.manual, Healthy: c.healthy, Detail: Failing(c.results),
		Results: rs, Failures: c.failures, Successes: c.successes, Cycles: c.cycles,
		Since: c.since, LastCycle: c.lastCycle, Cooldown: c.cooldown, Reentering: c.reentering,
	}
}

// State returns the current announcement state.
func (c *Controller) State() State {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.state
}

// Healthy reports the last cycle's verdict for the /status page: ok and
// "ok", or false and the failing checks. Before the first cycle it reports
// unhealthy so a node that has not proven itself never answers 20.
func (c *Controller) Healthy() (bool, string) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.lastCycle.IsZero() {
		return false, "starting"
	}
	return c.healthy, Failing(c.results)
}

// Run reconciles BIRD to the withdrawn starting state, then runs a cycle
// immediately and every Interval until ctx is cancelled. On cancellation it
// drains (not withdraws) with a 5 s budget and returns; see docs/health.md
// for why.
func (c *Controller) Run(ctx context.Context) error {
	c.reconcile(ctx)
	c.Tick(ctx)
	t := time.NewTicker(c.cfg.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return c.shutdown()
		case <-t.C:
			c.Tick(ctx)
		}
	}
}

// reconcile aligns the controller with the node's announcement state at
// startup. When the announcer can report it, the current state is adopted:
// a node that was announcing keeps announcing across a restart or deploy,
// and the regular checks decide what happens next (an unhealthy node is
// drained and withdrawn by the normal thresholds). Without a report the
// node has not proven itself and is withdrawn; the script is idempotent.
func (c *Controller) reconcile(ctx context.Context) {
	c.opMu.Lock()
	defer c.opMu.Unlock()
	if r, ok := c.ann.(StateReporter); ok {
		if st, err := r.State(ctx); err == nil {
			c.mu.Lock()
			c.state = st
			if st == StateAnnounced {
				// Count the adoption as a recovery so a healthy node is not
				// re-drained by the cooldown bookkeeping.
				c.successes = c.cfg.SuccessesToRecover
			}
			c.mu.Unlock()
			c.met.SetAnnounced(st == StateAnnounced)
			c.log.Info("bgp state adopted", "state", st.String(), "reason", "startup")
			return
		} else {
			c.log.Warn("bgp state unknown at startup; withdrawing", "err", err)
		}
	}
	if err := c.ann.Withdraw(ctx); err != nil {
		c.log.Error("bgp initial withdraw failed", "err", err)
		return
	}
	c.log.Info("bgp state reconciled", "state", StateWithdrawn.String(), "reason", "startup")
}

func (c *Controller) shutdown() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c.opMu.Lock()
	defer c.opMu.Unlock()
	if c.State() != StateAnnounced {
		return nil
	}
	return c.transition(ctx, StateDrained, "shutdown", false)
}

// Tick runs one check cycle and applies the state machine. It is what Run
// calls every Interval; tests call it directly.
func (c *Controller) Tick(ctx context.Context) {
	results := c.chk.Run(ctx)
	ok := AllOK(results)
	now := c.now()

	c.opMu.Lock()
	defer c.opMu.Unlock()

	c.mu.Lock()
	c.results = results
	c.healthy = ok
	c.lastCycle = now
	c.cycles++
	if ok {
		c.successes++
		c.failures = 0
	} else {
		c.failures++
		c.successes = 0
		c.reentering = false
	}
	st, manual, reentering := c.state, c.manual, c.reentering
	f, s := c.failures, c.successes
	elapsed := now.Sub(c.since)
	cool := c.cooldown
	c.mu.Unlock()

	c.met.SetHealthy(ok)
	if !ok {
		c.log.Warn("health check failed", "failures", f, "state", st.String(), "detail", Failing(results))
	} else if f == 0 && s == 1 {
		c.log.Info("health check recovered", "state", st.String())
	}
	if manual {
		return
	}

	recoverOK := s >= c.cfg.SuccessesToRecover && elapsed >= cool
	switch st {
	case StateAnnounced:
		if f >= c.cfg.FailuresToWithdraw {
			_ = c.transition(ctx, StateWithdrawn, fmt.Sprintf("%d consecutive failures", f), true)
		} else if f >= c.cfg.FailuresToDrain {
			_ = c.transition(ctx, StateDrained, fmt.Sprintf("%d consecutive failures", f), true)
		}
	case StateDrained:
		if f >= c.cfg.FailuresToWithdraw {
			_ = c.transition(ctx, StateWithdrawn, fmt.Sprintf("%d consecutive failures", f), true)
		} else if reentering && s >= 1 {
			_ = c.transition(ctx, StateAnnounced, "re-entry: healthy for one drained tick", true)
		} else if recoverOK {
			_ = c.transition(ctx, StateAnnounced, fmt.Sprintf("%d consecutive successes, cooldown %s elapsed", s, cool), true)
		}
	case StateWithdrawn:
		if recoverOK {
			_ = c.transition(ctx, StateDrained, fmt.Sprintf("%d consecutive successes, cooldown %s elapsed; re-entering drained", s, cool), true)
		}
	}
}

// transition applies the announcer calls for from->to and, on success,
// records the new state. On failure the state is unchanged and the next
// cycle retries. Callers hold opMu. auto marks a health-driven transition
// (subject to flap backoff bookkeeping).
func (c *Controller) transition(ctx context.Context, to State, reason string, auto bool) error {
	from := c.State()
	if from == to {
		return nil
	}
	var err error
	switch {
	case to == StateWithdrawn:
		err = c.ann.Withdraw(ctx)
	case to == StateDrained && from == StateAnnounced:
		err = c.ann.Drain(ctx)
	case to == StateDrained && from == StateWithdrawn:
		// bgp-announce has no "announce drained" verb; announce then drain.
		// The window between the two reloads is one birdc round-trip.
		if err = c.ann.Announce(ctx); err == nil {
			err = c.ann.Drain(ctx)
		}
	case to == StateAnnounced && from == StateDrained:
		err = c.ann.Undrain(ctx)
	case to == StateAnnounced && from == StateWithdrawn:
		err = c.ann.Announce(ctx)
	default:
		err = fmt.Errorf("no transition %s -> %s", from, to)
	}
	now := c.now()
	c.mu.Lock()
	f, s := c.failures, c.successes
	if err != nil {
		c.mu.Unlock()
		c.log.Error("bgp transition failed", "from", from.String(), "to", to.String(), "reason", reason, "failures", f, "successes", s, "err", err)
		return err
	}
	c.state = to
	c.since = now
	c.reentering = to == StateDrained && from == StateWithdrawn && auto
	if auto && from == StateAnnounced {
		// Flap backoff: an unplanned exit from announced within FlapWindow
		// of the previous one doubles the cooldown, capped at MaxBackoff; a
		// quiet period resets it to the base value.
		if !c.lastDrain.IsZero() && now.Sub(c.lastDrain) < c.cfg.FlapWindow {
			c.cooldown = min(c.cooldown*2, c.cfg.MaxBackoff)
		} else {
			c.cooldown = c.base
		}
		c.lastDrain = now
	}
	cool := c.cooldown
	c.mu.Unlock()
	c.met.SetAnnounced(to == StateAnnounced)
	c.log.Info("bgp state change", "from", from.String(), "to", to.String(), "reason", reason, "failures", f, "successes", s, "cooldown", cool)
	return nil
}

// ErrPinned is returned by Undrain when no operator pin is in effect.
var ErrPinned = errors.New("health: no operator pin in effect")

// Drain is the operator override (`forge admin pop drain`): the node is
// drained now (if announced) and stays drained or withdrawn, whatever the
// checks say, until Undrain.
func (c *Controller) Drain(ctx context.Context) error {
	c.opMu.Lock()
	defer c.opMu.Unlock()
	if c.State() == StateAnnounced {
		if err := c.transition(ctx, StateDrained, "operator drain", false); err != nil {
			return err
		}
	}
	c.pin(true, "drain")
	return nil
}

// Withdraw is the operator override for longer maintenance: withdraw now
// and stay withdrawn until Undrain.
func (c *Controller) Withdraw(ctx context.Context) error {
	c.opMu.Lock()
	defer c.opMu.Unlock()
	if err := c.transition(ctx, StateWithdrawn, "operator withdraw", false); err != nil {
		return err
	}
	c.pin(true, "withdraw")
	return nil
}

// Undrain releases the operator pin. The node re-enters through the normal
// hysteresis (K healthy cycles and the cooldown since the pin), which may
// be immediately at the next cycle if it has been healthy throughout.
func (c *Controller) Undrain(ctx context.Context) error {
	c.opMu.Lock()
	defer c.opMu.Unlock()
	c.mu.RLock()
	pinned := c.manual
	c.mu.RUnlock()
	if !pinned {
		return ErrPinned
	}
	c.pin(false, "undrain")
	return nil
}

func (c *Controller) pin(on bool, verb string) {
	c.mu.Lock()
	c.manual = on
	st := c.state
	c.mu.Unlock()
	c.log.Info("operator override", "verb", verb, "pinned", on, "state", st.String())
}
