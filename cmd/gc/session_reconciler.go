// session_reconciler.go implements the bead-driven reconciliation loop.
// It uses a wake/sleep model: for each session
// bead, compute whether the session should be awake, and manage lifecycle
// transitions using the Phase 2 building blocks.
//
// This reconciler uses desiredState (map[string]TemplateParams) for config
// queries and runtime.Provider directly for lifecycle operations. There
// is no dependency on agent types.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/clock"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/events"
	"github.com/gastownhall/gascity/internal/runtime"
	"github.com/gastownhall/gascity/internal/sessionlog"
	"github.com/gastownhall/gascity/internal/telemetry"
)

const maxIdleSleepProbesPerTick = 3

type wakeTarget struct {
	session *beads.Bead
	tp      TemplateParams
	alive   bool
}

// buildDepsMap extracts template dependency edges from config for topo ordering.
// Maps template QualifiedName -> list of dependency template QualifiedNames.
func buildDepsMap(cfg *config.City) map[string][]string {
	if cfg == nil {
		return nil
	}
	deps := make(map[string][]string)
	for _, a := range cfg.Agents {
		if len(a.DependsOn) > 0 {
			deps[a.QualifiedName()] = append([]string(nil), a.DependsOn...)
		}
	}
	return deps
}

// derivePoolDesired computes pool desired counts from the desired state map.
// Since buildDesiredState already ran evaluatePool, the number of instances
// per template in the desired state IS the desired count.
func derivePoolDesired(desiredState map[string]TemplateParams, cfg *config.City) map[string]int {
	if cfg == nil {
		return nil
	}
	counts := make(map[string]int)
	for _, tp := range desiredState {
		cfgAgent := findAgentByTemplate(cfg, tp.TemplateName)
		if cfgAgent != nil && cfgAgent.Pool != nil && !tp.ManualSession {
			counts[tp.TemplateName]++
		}
	}
	return counts
}

// allDependenciesAliveForTemplate checks that all template dependencies of a
// resolved logical template have at least one alive instance. Uses the
// runtime.Provider directly instead of agent types for liveness checks.
func allDependenciesAliveForTemplate(
	template string,
	cfg *config.City,
	desiredState map[string]TemplateParams,
	sp runtime.Provider,
	cityName string,
	store beads.Store,
) bool {
	cfgAgent := findAgentByTemplate(cfg, template)
	if cfgAgent == nil || len(cfgAgent.DependsOn) == 0 {
		return true
	}
	for _, dep := range cfgAgent.DependsOn {
		depCfg := findAgentByTemplate(cfg, dep)
		if depCfg == nil {
			continue // dependency not in config — skip
		}
		if !dependencyTemplateAlive(dep, cfg, desiredState, sp, cityName, store) {
			return false
		}
	}
	return true
}

// allDependenciesAlive checks that all template dependencies of a session
// have at least one alive instance. Uses the runtime.Provider directly
// instead of agent types for liveness checks.
func allDependenciesAlive(
	session beads.Bead,
	cfg *config.City,
	desiredState map[string]TemplateParams,
	sp runtime.Provider,
	cityName string,
	store beads.Store,
) bool {
	return allDependenciesAliveForTemplate(normalizedSessionTemplate(session, cfg), cfg, desiredState, sp, cityName, store)
}

// reconcileSessionBeads performs bead-driven reconciliation using wake/sleep
// semantics. For each session bead, it determines if the session should be
// awake (has a matching entry in the desired state) and manages lifecycle
// transitions using the Phase 2 building blocks.
//
// The function assumes session beads are already synced (syncSessionBeads
// called before this function). When the bead reconciler is active,
// syncSessionBeads does NOT close orphan/suspended beads (skipClose=true),
// so the sessions slice may include beads with no matching desired entry.
// These are handled by the orphan/suspended drain phase.
//
// desiredState maps sessionName → TemplateParams for all agents that should
// be running. Built by buildDesiredState from config + scale_check results.
//
// configuredNames is the set of ALL configured agent session names (including
// suspended agents). Used to distinguish "orphaned" (removed from config)
// from "suspended" (still in config, not runnable) when closing beads.
//
// Returns the number of sessions woken this tick.
func reconcileSessionBeads(
	ctx context.Context,
	sessions []beads.Bead,
	desiredState map[string]TemplateParams,
	configuredNames map[string]bool,
	cfg *config.City,
	sp runtime.Provider,
	store beads.Store,
	dops drainOps,
	workSet map[string]bool,
	readyWaitSet map[string]bool,
	dt *drainTracker,
	poolDesired map[string]int,
	cityName string,
	it idleTracker,
	st stuckTracker,
	clk clock.Clock,
	rec events.Recorder,
	startupTimeout time.Duration,
	startupProbeTimeout time.Duration,
	driftDrainTimeout time.Duration,
	stdout, stderr io.Writer,
) int {
	deps := buildDepsMap(cfg)

	// Phase 0: Heal expired timers on all sessions.
	for i := range sessions {
		healExpiredTimers(&sessions[i], store, clk)
	}

	// Topo-order sessions by template dependencies.
	ordered := topoOrder(sessions, deps)

	// Build session ID -> *beads.Bead lookup for advanceSessionDrains.
	// These pointers intentionally alias into the ordered slice so that
	// mutations in Phase 1 (healState, clearWakeFailures, etc.) are
	// visible to Phase 2's advanceSessionDrains via this map.
	beadByID := make(map[string]*beads.Bead, len(ordered))
	for i := range ordered {
		beadByID[ordered[i].ID] = &ordered[i]
	}

	// Phase 1: Forward pass (topo order) — wake sessions, handle alive state.
	var startCandidates []startCandidate
	var wakeTargets []wakeTarget
	for i := range ordered {
		session := &ordered[i]

		// Skip beads with unrecognized states. This enables forward-compatible
		// rollback: if a newer version writes "draining" or "archived", the
		// older reconciler ignores those beads rather than crashing.
		if !isKnownState(*session) {
			fmt.Fprintf(stderr, "session reconciler: skipping %s with unknown state %q\n", //nolint:errcheck // best-effort stderr
				session.Metadata["session_name"], session.Metadata["state"])
			continue
		}

		name := session.Metadata["session_name"]
		tp, desired := desiredState[name]

		// Orphan/suspended: bead exists but not in desired state.
		// Handle BEFORE heal/stability to avoid false crash detection —
		// a running session that leaves the desired set is not a crash.
		if !desired {
			providerAlive := sp.IsRunning(name)
			// Heal state using provider liveness, not agent membership.
			healState(session, providerAlive, store)
			if providerAlive {
				reason := "orphaned"
				if configuredNames[name] {
					reason = "suspended"
				}
				beginSessionDrain(*session, sp, dt, reason, clk, defaultDrainTimeout)
				fmt.Fprintf(stdout, "Draining session '%s': %s\n", name, reason) //nolint:errcheck
			} else {
				// Not running and not desired — close the bead.
				reason := "orphaned"
				if configuredNames[name] {
					reason = "suspended"
				}
				closeBead(store, session.ID, reason, clk.Now().UTC(), stderr)
			}
			continue
		}

		// Liveness includes zombie detection: tmux session exists AND
		// the expected child process is alive (when ProcessNames configured).
		running := sp.IsRunning(name)
		alive := running && sp.ProcessAlive(name, tp.Hints.ProcessNames)

		// Zombie capture: session exists but process dead — grab scrollback for forensics.
		if running && !alive {
			if output, err := sp.Peek(name, 50); err == nil && output != "" {
				rec.Record(events.Event{
					Type:    events.SessionCrashed,
					Actor:   "gc",
					Subject: tp.DisplayName(),
					Message: output,
				})
				telemetry.RecordAgentCrash(context.Background(), tp.DisplayName(), output)
				recordSessionTokens(*session, tp.DisplayName(), cfg, stderr)
			}
		}
		if alive && shouldRollbackPendingCreate(session) && !runningSessionMatchesPendingCreate(session, name, sp) {
			fmt.Fprintf(stderr, "session reconciler: rolling back pending create %s: live runtime belongs to another session\n", name) //nolint:errcheck
			rollbackPendingCreate(session, store, clk.Now().UTC(), stderr)
			continue
		}

		policy := resolveSessionSleepPolicy(*session, cfg, sp)

		// Heal advisory state metadata.
		healState(session, alive, store)
		if recoverPendingIdleSleep(session, store, running, clk) {
			alive = false
		}
		reconcileDetachedAt(session, store, policy, alive, sp, clk)

		// Stability check: detect rapid exit (crash).
		if checkStability(session, cfg, alive, dt, store, clk) {
			continue // crash recorded, skip further processing
		}

		// Startup probe: detect sessions alive but stuck (provider at prompt,
		// quota wall, etc.). Must run BEFORE clearing wake failures so that a
		// stuck session doesn't reset its counter.
		if checkStartupProbe(*session, alive, dops, clk, startupProbeTimeout) {
			fmt.Fprintf(stderr, "session reconciler: startup probe failed for %s (no ack within %s)\n", name, startupProbeTimeout) //nolint:errcheck
			rec.Record(events.Event{
				Type:    events.SessionStartupDead,
				Actor:   "gc",
				Subject: tp.DisplayName(),
				Message: fmt.Sprintf("no startup ack within %s", startupProbeTimeout),
			})
			if err := sp.Stop(name); err != nil {
				fmt.Fprintf(stderr, "session reconciler: stopping stuck %s: %v\n", name, err) //nolint:errcheck
			}
			recordWakeFailure(session, store, clk)
			_ = store.SetMetadata(session.ID, "last_woke_at", "")
			session.Metadata["last_woke_at"] = ""
			continue
		}

		// Quota detection: session alive but provider account exhausted.
		// Kill the session so the reconciler restarts it, potentially selecting
		// a different provider via the random provider strategy.
		// Unlike crash recovery, no wake failure is recorded — quota is not a
		// crash and we want immediate restart without quarantine.
		if alive && stableLongEnough(*session, clk) {
			if checkQuotaExhausted(*session, cfg, stderr) {
				_ = store.SetMetadataBatch(session.ID, map[string]string{
					"session_key": "",
					"last_woke_at": "",
				})
				session.Metadata["session_key"] = ""
				session.Metadata["last_woke_at"] = ""
				if err := sp.Stop(name); err != nil {
					fmt.Fprintf(stderr, "session reconciler: stopping quota-exhausted %s: %v\n", name, err) //nolint:errcheck
				} else {
					fmt.Fprintf(stdout, "Stopped quota-exhausted session '%s'\n", name) //nolint:errcheck
					rec.Record(events.Event{
						Type:    events.SessionQuotaKilled,
						Actor:   "gc",
						Subject: tp.DisplayName(),
						Message: "provider account quota exhausted",
					})
					recordSessionTokens(*session, tp.DisplayName(), cfg, stderr)
				}
				continue
			}
		}

		// Clear wake failures for sessions that have been stable long enough.
		if alive && stableLongEnough(*session, clk) {
			clearWakeFailures(session, store)
		}
		if alive && shouldRollbackPendingCreate(session) {
			if err := clearPendingCreateClaim(session, store); err != nil {
				fmt.Fprintf(stderr, "session reconciler: clearing pending create claim for %s: %v\n", name, err) //nolint:errcheck
			}
		}

		// Drain-ack: agent signaled it's done (gc runtime drain-ack).
		// Stop the session immediately so the pool can reclaim the slot
		// and a fresh session handles the next work item.
		// Clear session_key so the next wake starts a fresh conversation
		// instead of resuming the completed one (the agent already exited).
		if alive && dops != nil {
			if acked, _ := dops.isDrainAcked(name); acked {
				_ = dops.clearDrain(name)
				if session.Metadata["session_key"] != "" {
					_ = store.SetMetadataBatch(session.ID, map[string]string{
						"session_key":                "",
						"started_config_hash":        "",
						"continuation_reset_pending": "true",
					})
					session.Metadata["session_key"] = ""
					session.Metadata["started_config_hash"] = ""
					session.Metadata["continuation_reset_pending"] = "true"
				}
				if err := sp.Stop(name); err != nil {
					fmt.Fprintf(stderr, "session reconciler: stopping drain-acked %s: %v\n", name, err) //nolint:errcheck
				} else {
					fmt.Fprintf(stdout, "Stopped drain-acked session '%s'\n", name) //nolint:errcheck
					rec.Record(events.Event{
						Type:    events.SessionStopped,
						Actor:   "gc",
						Subject: tp.DisplayName(),
						Message: "drain acknowledged by agent",
					})
					recordSessionTokens(*session, tp.DisplayName(), cfg, stderr)
				}
				continue
			}
		}

		// Restart-requested: agent asked for a fresh session
		// (gc runtime request-restart). Clear session_key so the next
		// wake starts a fresh conversation, then stop immediately;
		// the next tick will re-create and re-wake.
		if alive && dops != nil {
			if requested, _ := dops.isRestartRequested(name); requested {
				_ = dops.clearRestartRequested(name)
				// Clear session_key so the next start gets a fresh conversation
				// instead of resuming the old one. Mirrors recordWakeFailure().
				if session.Metadata["session_key"] != "" {
					_ = store.SetMetadataBatch(session.ID, map[string]string{
						"session_key":                "",
						"started_config_hash":        "",
						"continuation_reset_pending": "true",
					})
					session.Metadata["session_key"] = ""
					session.Metadata["started_config_hash"] = ""
					session.Metadata["continuation_reset_pending"] = "true"
				}
				if err := sp.Stop(name); err != nil {
					fmt.Fprintf(stderr, "session reconciler: stopping restart-requested %s: %v\n", name, err) //nolint:errcheck
				} else {
					fmt.Fprintf(stdout, "Stopped restart-requested session '%s'\n", name) //nolint:errcheck
					recordSessionTokens(*session, tp.DisplayName(), cfg, stderr)
				}
				continue
			}
		}

		// Config drift: if alive and config changed, drain for restart.
		// Live-only drift: re-apply session_live without restart.
		if alive {
			template := tp.TemplateName
			if template == "" {
				template = normalizedSessionTemplate(*session, cfg)
			}
			storedHash := session.Metadata["config_hash"]
			if sh := session.Metadata["started_config_hash"]; sh != "" {
				storedHash = sh
			}
			if template != "" && storedHash != "" {
				cfgAgent := findAgentByTemplate(cfg, template)
				if cfgAgent != nil {
					agentCfg := templateParamsToConfig(tp)
					currentHash := runtime.CoreFingerprint(agentCfg)
					if storedHash != currentHash {
						// Defer config-drift drain while a user is attached.
						// Killing a session mid-conversation is disruptive;
						// the drift will be applied when the user detaches.
						if sp.IsAttached(name) {
							continue
						}
						ddt := driftDrainTimeout
						if ddt <= 0 {
							ddt = defaultDrainTimeout
						}
						beginSessionDrain(*session, sp, dt, "config-drift", clk, ddt)
						fmt.Fprintf(stdout, "Draining session '%s': config-drift\n", name) //nolint:errcheck
						rec.Record(events.Event{
							Type:    events.SessionDraining,
							Actor:   "gc",
							Subject: tp.DisplayName(),
							Message: "config drift detected",
						})
						continue
					}

					// Core config matches — check live-only drift.
					storedLive := session.Metadata["live_hash"]
					if sl := session.Metadata["started_live_hash"]; sl != "" {
						storedLive = sl
					}
					if storedLive != "" {
						currentLive := runtime.LiveFingerprint(agentCfg)
						if storedLive != currentLive {
							fmt.Fprintf(stdout, "Live config changed for '%s', re-applying...\n", tp.DisplayName()) //nolint:errcheck
							if err := sp.RunLive(name, agentCfg); err != nil {
								fmt.Fprintf(stderr, "session reconciler: RunLive %s: %v\n", name, err) //nolint:errcheck
							} else {
								_ = store.SetMetadataBatch(session.ID, map[string]string{
									"live_hash":         currentLive,
									"started_live_hash": currentLive,
								})
								rec.Record(events.Event{
									Type:    events.SessionUpdated,
									Actor:   "gc",
									Subject: tp.DisplayName(),
									Message: "session_live re-applied",
								})
							}
						}
					}
				}
			}
		}

		// Idle timeout: restart sessions idle longer than configured threshold.
		if it != nil && alive && it.checkIdle(name, sp, clk.Now()) {
			fmt.Fprintf(stderr, "session reconciler: idle timeout for %s\n", tp.DisplayName()) //nolint:errcheck // best-effort stderr
			if err := sp.Stop(name); err != nil {
				fmt.Fprintf(stderr, "session reconciler: stopping idle %s: %v\n", name, err) //nolint:errcheck // best-effort stderr
			} else {
				_ = sp.ClearScrollback(name)
				rec.Record(events.Event{
					Type:    events.SessionIdleKilled,
					Actor:   "gc",
					Subject: tp.DisplayName(),
				})
				telemetry.RecordAgentIdleKill(context.Background(), tp.DisplayName())
				recordSessionTokens(*session, tp.DisplayName(), cfg, stderr)
				// Mark for immediate re-wake on this same tick by clearing
				// last_woke_at and setting state to asleep. The wake logic
				// below will pick it up.
				_ = store.SetMetadataBatch(session.ID, map[string]string{
					"state": "asleep", "last_woke_at": "", "sleep_reason": "idle-timeout",
				})
				session.Metadata["state"] = "asleep"
				session.Metadata["last_woke_at"] = ""
				session.Metadata["sleep_reason"] = "idle-timeout"
				alive = false
			}
			// Fall through to wakeReasons — it will re-wake immediately if config present
		}

		// Stuck timeout: restart sessions alive longer than configured threshold
		// since their last wake. Catches infinite thinking loops and similar
		// runaway states where idle_timeout would not trigger (the agent is
		// generating output, so session_activity stays recent). Clears
		// session_key to force a fresh conversation rather than resuming
		// the stuck context.
		if st != nil && alive && st.checkStuck(*session, clk.Now()) {
			fmt.Fprintf(stderr, "session reconciler: stuck timeout for %s\n", tp.DisplayName()) //nolint:errcheck // best-effort stderr
			if err := sp.Stop(name); err != nil {
				fmt.Fprintf(stderr, "session reconciler: stopping stuck %s: %v\n", name, err) //nolint:errcheck // best-effort stderr
			} else {
				_ = sp.ClearScrollback(name)
				rec.Record(events.Event{
					Type:    events.SessionStuckKilled,
					Actor:   "gc",
					Subject: tp.DisplayName(),
				})
				telemetry.RecordAgentStuckKill(context.Background(), tp.DisplayName())
				recordSessionTokens(*session, tp.DisplayName(), cfg, stderr)
				// Clear session_key so the next wake starts a fresh conversation
				// rather than resuming the stuck loop. Set state to "active" so
				// WakeSession fires and the session is immediately restarted on
				// the same tick (unlike idle_timeout which sleeps the session).
				_ = store.SetMetadataBatch(session.ID, map[string]string{
					"state":        "active",
					"last_woke_at": "",
					"session_key":  "",
				})
				session.Metadata["state"] = "active"
				session.Metadata["last_woke_at"] = ""
				session.Metadata["session_key"] = ""
				alive = false
			}
			// Fall through to wakeTargets — it will re-wake immediately if config present
		}

		wakeTargets = append(wakeTargets, wakeTarget{session: session, tp: tp, alive: alive})
	}

	evalInput := make([]beads.Bead, len(wakeTargets))
	for i, target := range wakeTargets {
		evalInput[i] = *target.session
	}
	wakeEvals := computeWakeEvaluations(evalInput, cfg, sp, poolDesired, workSet, readyWaitSet, clk)
	idleProbeTargets := selectIdleProbeTargets(wakeTargets, wakeEvals, dt)
	launchIdleProbes(ctx, idleProbeTargets, wakeTargets, dt, sp, clk)

	for _, target := range wakeTargets {
		eval, ok := wakeEvals[target.session.ID]
		if !ok {
			eval = wakeEvaluation{Policy: resolveSessionSleepPolicy(*target.session, cfg, sp)}
		}
		persistSleepPolicyMetadata(target.session, store, eval.Policy, eval.ConfigSuppressed)
		shouldWake := len(eval.Reasons) > 0

		if shouldWake && !target.alive {
			// Session should be awake but isn't — wake it.
			if sessionIsQuarantined(*target.session, clk) {
				continue // crash-loop protection
			}
			startCandidates = append(startCandidates, startCandidate{
				session: target.session,
				tp:      target.tp,
				order:   len(startCandidates),
			})
		}

		if shouldWake && target.alive {
			// Session is correctly awake. Cancel any non-drift drain
			// (handles scale-back-up: agent returns to desired set while draining).
			cancelSessionDrain(*target.session, dt)
			clearCompletedIdleProbe(target.session.ID, dt)
			if target.session.Metadata["sleep_intent"] == "idle-stop-pending" {
				_ = store.SetMetadata(target.session.ID, "sleep_intent", "")
				target.session.Metadata["sleep_intent"] = ""
			}
		}

		if !shouldWake && target.alive {
			// No reason to be awake — begin drain.
			reason := "no-wake-reason"
			intent := target.session.Metadata["sleep_intent"]
			switch {
			case intent == "idle-stop-pending":
				reason = "idle"
			case intent != "":
				reason = intent
			case eval.ConfigSuppressed && eval.Policy.enabled():
				reason = "idle"
			}
			if reason != "idle" {
				clearCompletedIdleProbe(target.session.ID, dt)
			}
			if reason == "idle" && dt.get(target.session.ID) == nil {
				if intent != "idle-stop-pending" && !shouldBeginIdleDrain(target.session, eval, dt, sp) {
					continue
				}
				if intent != "idle-stop-pending" {
					markIdleSleepPending(target.session, store)
				}
			}
			beginSessionDrain(*target.session, sp, dt, reason, clk, defaultDrainTimeout)
			fmt.Fprintf(stdout, "Draining session '%s': %s\n", target.session.Metadata["session_name"], reason) //nolint:errcheck
		}
	}

	plannedWakes := executePlannedStarts(
		ctx, startCandidates, cfg, desiredState, sp, store, cityName,
		clk, rec, startupTimeout, stdout, stderr,
	)

	// Phase 2: Advance all in-flight drains.
	sessionLookup := func(id string) *beads.Bead {
		return beadByID[id]
	}
	advanceSessionDrainsWithSessions(dt, sp, store, sessionLookup, ordered, wakeEvals, cfg, poolDesired, workSet, readyWaitSet, clk)
	clearMissingIdleProbes(dt, beadByID)

	return plannedWakes
}

func shouldBeginIdleDrain(
	session *beads.Bead,
	eval wakeEvaluation,
	dt *drainTracker,
	sp runtime.Provider,
) bool {
	if session == nil {
		return false
	}
	if eval.Policy.Class == config.SessionSleepNonInteractive {
		return true
	}
	if eval.Policy.Capability != runtime.SessionSleepCapabilityFull || sp == nil {
		return false
	}
	probe, ok := dt.idleProbe(session.ID)
	if !ok || !probe.ready {
		return false
	}
	defer dt.clearIdleProbe(session.ID)
	if !probe.success {
		return false
	}
	lastActivity, err := sp.GetLastActivity(session.Metadata["session_name"])
	if err != nil {
		return false
	}
	return lastActivity.IsZero() || !lastActivity.After(probe.completedAt)
}

func selectIdleProbeTargets(
	wakeTargets []wakeTarget,
	wakeEvals map[string]wakeEvaluation,
	dt *drainTracker,
) map[string]bool {
	targets := make(map[string]bool)
	if dt == nil {
		return targets
	}
	var candidates []string
	// Snapshot drain/probe state under one lock. Do not call other
	// drainTracker helpers while holding dt.mu.
	dt.mu.Lock()
	defer dt.mu.Unlock()
	activeProbes := 0
	for _, probe := range dt.idleProbes {
		if probe != nil && !probe.ready {
			activeProbes++
		}
	}
	limit := maxIdleSleepProbesPerTick - activeProbes
	if limit <= 0 {
		return targets
	}
	for _, target := range wakeTargets {
		if target.session == nil || !target.alive {
			continue
		}
		if target.session.Metadata["sleep_intent"] != "" {
			continue
		}
		if dt.drains[target.session.ID] != nil {
			continue
		}
		if dt.idleProbes[target.session.ID] != nil {
			continue
		}
		eval, ok := wakeEvals[target.session.ID]
		if !ok || len(eval.Reasons) > 0 || !eval.ConfigSuppressed || !eval.Policy.enabled() {
			continue
		}
		if eval.Policy.Class == config.SessionSleepNonInteractive {
			continue
		}
		candidates = append(candidates, target.session.ID)
	}
	if len(candidates) == 0 {
		if activeProbes == 0 {
			dt.idleProbeCursor = 0
		}
		return targets
	}
	start := dt.idleProbeCursor % len(candidates)
	if limit > len(candidates) {
		limit = len(candidates)
	}
	for i := 0; i < limit; i++ {
		targets[candidates[(start+i)%len(candidates)]] = true
	}
	dt.idleProbeCursor = (start + limit) % len(candidates)
	return targets
}

func launchIdleProbes(
	ctx context.Context,
	idleProbeTargets map[string]bool,
	wakeTargets []wakeTarget,
	dt *drainTracker,
	sp runtime.Provider,
	clk clock.Clock,
) {
	if len(idleProbeTargets) == 0 || dt == nil || sp == nil {
		return
	}
	wp, ok := sp.(runtime.IdleWaitProvider)
	if !ok {
		return
	}
	for _, target := range wakeTargets {
		if target.session == nil || !idleProbeTargets[target.session.ID] {
			continue
		}
		name := target.session.Metadata["session_name"]
		probe := dt.startIdleProbe(target.session.ID)
		if name == "" || probe == nil {
			continue
		}
		dt.beginIdleProbe()
		go func(beadID, sessionName string, probe *idleProbeState) {
			defer dt.doneIdleProbe()
			err := wp.WaitForIdle(ctx, sessionName, idleSleepProbeTimeout)
			dt.finishIdleProbe(beadID, probe, err == nil, clk.Now().UTC())
		}(target.session.ID, name, probe)
	}
}

func clearCompletedIdleProbe(beadID string, dt *drainTracker) {
	if dt == nil {
		return
	}
	probe, ok := dt.idleProbe(beadID)
	if ok && probe.ready {
		dt.clearIdleProbe(beadID)
	}
}

func clearMissingIdleProbes(dt *drainTracker, beadByID map[string]*beads.Bead) {
	if dt == nil {
		return
	}
	dt.mu.Lock()
	var stale []string
	for id := range dt.idleProbes {
		if beadByID[id] == nil {
			stale = append(stale, id)
		}
	}
	dt.mu.Unlock()
	for _, id := range stale {
		dt.clearIdleProbe(id)
	}
}

// resolveTaskWorkDir checks the agent's assigned task beads for a work_dir
// metadata field. If a task bead has work_dir set and the directory exists
// on disk, that path is returned. This lets the reconciler start the agent
// in the worktree that the previous session (or this session's prior run)
// created, without any prompt-side logic.
func resolveTaskWorkDir(store beads.Store, agentName string) string {
	assigned, err := store.ListByAssignee(agentName, "in_progress", 0)
	if err != nil {
		return ""
	}
	for _, b := range assigned {
		wd := b.Metadata["work_dir"]
		if wd != "" {
			if info, err := os.Stat(wd); err == nil && info.IsDir() {
				return wd
			}
		}
	}
	return ""
}

// resolveSessionCommand returns the command to use when starting a session.
// On first start (no prior session exists), it uses SessionIDFlag to create a
// session with the given key as its ID. On subsequent wakes, it uses
// resolveResumeCommand to resume the existing session.
func resolveSessionCommand(command, sessionKey string, rp *config.ResolvedProvider, firstStart bool) string {
	if firstStart && rp.SessionIDFlag != "" {
		return command + " " + rp.SessionIDFlag + " " + sessionKey
	}
	return resolveResumeCommand(command, sessionKey, rp)
}

// recordSessionTokens extracts token totals from a stopped session's JSONL log
// and records them to OTel. Best-effort: errors are logged to stderr but do not
// block the reconciler.
func recordSessionTokens(session beads.Bead, agentName string, cfg *config.City, stderr io.Writer) {
	workDir := session.Metadata["work_dir"]
	if workDir == "" {
		return
	}
	if abs, err := filepath.Abs(workDir); err == nil {
		workDir = abs
	}

	searchPaths := sessionlog.DefaultSearchPaths()
	if cfg != nil {
		searchPaths = sessionlog.MergeSearchPaths(cfg.Daemon.ObservePaths)
	}

	sessionKey := session.Metadata["session_key"]
	var sessionFile string
	if sessionKey != "" {
		sessionFile = sessionlog.FindSessionFileByID(searchPaths, workDir, sessionKey)
	} else {
		sessionFile = sessionlog.FindSessionFile(searchPaths, workDir)
	}
	if sessionFile == "" {
		return
	}

	ctx := context.Background()
	totals, err := sessionlog.ExtractAllTokenTotals(sessionFile)
	if err != nil {
		fmt.Fprintf(stderr, "session reconciler: token extraction for %s: %v\n", agentName, err) //nolint:errcheck
		return
	}
	for _, t := range totals {
		telemetry.RecordTokenUsage(ctx, agentName, t.Model, t.InputTokens, t.OutputTokens)
	}
}

// checkQuotaExhausted returns true if the session's transcript shows that the
// provider account is quota-exhausted. It reads the tail of the session's JSONL
// log and checks whether the last few assistant/result entries all contain
// quota error patterns. Returns false on any error (fail-safe).
func checkQuotaExhausted(session beads.Bead, cfg *config.City, stderr io.Writer) bool {
	workDir := session.Metadata["work_dir"]
	if workDir == "" {
		return false
	}
	if abs, err := filepath.Abs(workDir); err == nil {
		workDir = abs
	}

	searchPaths := sessionlog.DefaultSearchPaths()
	if cfg != nil {
		searchPaths = sessionlog.MergeSearchPaths(cfg.Daemon.ObservePaths)
	}

	sessionKey := session.Metadata["session_key"]
	var sessionFile string
	if sessionKey != "" {
		sessionFile = sessionlog.FindSessionFileByID(searchPaths, workDir, sessionKey)
	} else {
		sessionFile = sessionlog.FindSessionFile(searchPaths, workDir)
	}
	if sessionFile == "" {
		return false
	}

	exhausted := sessionlog.ScanForQuotaExhausted(sessionFile, 0)
	if exhausted {
		fmt.Fprintf(stderr, "session reconciler: quota exhausted detected for %s (file: %s)\n", //nolint:errcheck
			session.Metadata["session_name"], sessionFile)
	}
	return exhausted
}

// resolveResumeCommand returns the command to use when resuming a session.
// Priority: explicit resume_command (with {{.SessionKey}} expansion) >
// ResumeFlag/ResumeStyle auto-construction > original command unchanged.
func resolveResumeCommand(command, sessionKey string, rp *config.ResolvedProvider) string {
	// Explicit resume_command takes precedence.
	if rp.ResumeCommand != "" {
		return strings.ReplaceAll(rp.ResumeCommand, "{{.SessionKey}}", sessionKey)
	}
	// Fall back to ResumeFlag/ResumeStyle auto-construction.
	if rp.ResumeFlag == "" {
		return command
	}
	switch rp.ResumeStyle {
	case "subcommand":
		parts := strings.SplitN(command, " ", 2)
		if len(parts) == 2 {
			return parts[0] + " " + rp.ResumeFlag + " " + sessionKey + " " + parts[1]
		}
		return command + " " + rp.ResumeFlag + " " + sessionKey
	default: // "flag"
		return command + " " + rp.ResumeFlag + " " + sessionKey
	}
}
