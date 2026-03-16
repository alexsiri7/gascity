package main

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/agent"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/clock"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/events"
	"github.com/gastownhall/gascity/internal/runtime"
)

// reconcilerTestEnv holds common test infrastructure.
type reconcilerTestEnv struct {
	store        beads.Store
	sp           *runtime.Fake
	dt           *drainTracker
	clk          *clock.Fake
	rec          events.Recorder
	stdout       bytes.Buffer
	stderr       bytes.Buffer
	cfg          *config.City
	desiredState map[string]TemplateParams
}

func newReconcilerTestEnv() *reconcilerTestEnv {
	return &reconcilerTestEnv{
		store:        beads.NewMemStore(),
		sp:           runtime.NewFake(),
		dt:           newDrainTracker(),
		clk:          &clock.Fake{Time: time.Date(2026, 3, 8, 12, 0, 0, 0, time.UTC)},
		rec:          events.Discard,
		cfg:          &config.City{},
		desiredState: make(map[string]TemplateParams),
	}
}

// addDesired registers a session in the desired state and optionally starts
// it in the provider. Returns the TemplateParams for further customization.
func (e *reconcilerTestEnv) addDesired(name, template string, running bool) {
	tp := TemplateParams{
		Command:      "test-cmd",
		SessionName:  name,
		TemplateName: template,
	}
	e.desiredState[name] = tp
	if running {
		_ = e.sp.Start(context.Background(), name, runtime.Config{Command: "test-cmd"})
	}
}

// addDesiredWithConfig registers a session with a custom runtime.Config.
func (e *reconcilerTestEnv) addDesiredWithConfig(name, template string, running bool, cmd string) {
	tp := TemplateParams{
		Command:      cmd,
		SessionName:  name,
		TemplateName: template,
	}
	e.desiredState[name] = tp
	if running {
		_ = e.sp.Start(context.Background(), name, runtime.Config{Command: cmd})
	}
}

// addDesiredLive registers a session with custom session_live config.
func (e *reconcilerTestEnv) addDesiredLive(name, template string, running bool, live []string) {
	tp := TemplateParams{
		Command:      "test-cmd",
		SessionName:  name,
		TemplateName: template,
		Hints:        agent.StartupHints{SessionLive: live},
	}
	e.desiredState[name] = tp
	if running {
		_ = e.sp.Start(context.Background(), name, runtime.Config{Command: "test-cmd", SessionLive: live})
	}
}

func (e *reconcilerTestEnv) createSessionBead(name, template string) beads.Bead {
	meta := map[string]string{
		"session_name":   name,
		"agent_name":     name,
		"template":       template,
		"config_hash":    runtime.CoreFingerprint(runtime.Config{Command: "test-cmd"}),
		"live_hash":      runtime.LiveFingerprint(runtime.Config{Command: "test-cmd"}),
		"generation":     "1",
		"instance_token": "test-token",
		"state":          "asleep",
	}
	b, err := e.store.Create(beads.Bead{
		Title:    name,
		Type:     sessionBeadType,
		Labels:   []string{sessionBeadLabel},
		Metadata: meta,
	})
	if err != nil {
		panic("creating test bead: " + err.Error())
	}
	return b
}

func (e *reconcilerTestEnv) reconcile(sessions []beads.Bead) int {
	cfgNames := configuredSessionNames(e.cfg, "", e.store)
	return reconcileSessionBeads(
		context.Background(), sessions, e.desiredState, cfgNames, e.cfg, e.sp,
		e.store, nil, nil, e.dt, map[string]int{}, "",
		e.clk, e.rec, 0, 0, &e.stdout, &e.stderr,
	)
}

// --- buildDepsMap tests ---

func TestBuildDepsMap_NilConfig(t *testing.T) {
	deps := buildDepsMap(nil)
	if deps != nil {
		t.Errorf("expected nil, got %v", deps)
	}
}

func TestBuildDepsMap_NoDeps(t *testing.T) {
	cfg := &config.City{
		Agents: []config.Agent{
			{Name: "a"},
			{Name: "b"},
		},
	}
	deps := buildDepsMap(cfg)
	if len(deps) != 0 {
		t.Errorf("expected empty map, got %v", deps)
	}
}

func TestBuildDepsMap_WithDeps(t *testing.T) {
	cfg := &config.City{
		Agents: []config.Agent{
			{Name: "worker", DependsOn: []string{"db"}},
			{Name: "db"},
		},
	}
	deps := buildDepsMap(cfg)
	if len(deps) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(deps))
	}
	if len(deps["worker"]) != 1 || deps["worker"][0] != "db" {
		t.Errorf("expected worker -> [db], got %v", deps["worker"])
	}
}

// --- derivePoolDesired tests ---

func TestDerivePoolDesired_NilConfig(t *testing.T) {
	result := derivePoolDesired(nil, nil)
	if result != nil {
		t.Errorf("expected nil, got %v", result)
	}
}

func TestDerivePoolDesired_CountsPoolInstances(t *testing.T) {
	cfg := &config.City{
		Agents: []config.Agent{
			{Name: "worker", Pool: &config.PoolConfig{Min: 1, Max: 5}},
			{Name: "overseer"},
		},
	}
	desired := map[string]TemplateParams{
		"worker-1": {TemplateName: "worker"},
		"worker-2": {TemplateName: "worker"},
		"worker-3": {TemplateName: "worker"},
		"overseer": {TemplateName: "overseer"},
	}
	result := derivePoolDesired(desired, cfg)
	if result["worker"] != 3 {
		t.Errorf("expected worker desired=3, got %d", result["worker"])
	}
	// Non-pool agents should not appear.
	if _, ok := result["overseer"]; ok {
		t.Error("non-pool agent should not be in poolDesired")
	}
}

// --- allDependenciesAlive tests ---

func TestAllDependenciesAlive_NoDeps(t *testing.T) {
	session := beads.Bead{Metadata: map[string]string{"template": "worker"}}
	cfg := &config.City{Agents: []config.Agent{{Name: "worker"}}}
	sp := runtime.NewFake()
	if !allDependenciesAlive(session, cfg, nil, sp, "test", nil) {
		t.Error("no deps should return true")
	}
}

func TestAllDependenciesAlive_DepAlive(t *testing.T) {
	session := beads.Bead{Metadata: map[string]string{"template": "worker"}}
	cfg := &config.City{
		Agents: []config.Agent{
			{Name: "worker", DependsOn: []string{"db"}},
			{Name: "db"},
		},
	}
	sp := runtime.NewFake()
	_ = sp.Start(context.Background(), "db", runtime.Config{})
	desired := map[string]TemplateParams{
		"db": {TemplateName: "db"},
	}
	if !allDependenciesAlive(session, cfg, desired, sp, "test", nil) {
		t.Error("dep is alive, should return true")
	}
}

func TestAllDependenciesAlive_DepDead(t *testing.T) {
	session := beads.Bead{Metadata: map[string]string{"template": "worker"}}
	cfg := &config.City{
		Agents: []config.Agent{
			{Name: "worker", DependsOn: []string{"db"}},
			{Name: "db"},
		},
	}
	sp := runtime.NewFake()
	desired := map[string]TemplateParams{
		"db": {TemplateName: "db"},
	}
	if allDependenciesAlive(session, cfg, desired, sp, "test", nil) {
		t.Error("dep is dead, should return false")
	}
}

// --- reconcileSessionBeads tests ---

func TestReconcileSessionBeads_WakesDeadSession(t *testing.T) {
	env := newReconcilerTestEnv()
	env.cfg = &config.City{Agents: []config.Agent{{Name: "worker"}}}
	env.addDesired("worker", "worker", false)
	session := env.createSessionBead("worker", "worker")

	woken := env.reconcile([]beads.Bead{session})

	if woken != 1 {
		t.Errorf("expected 1 woken, got %d", woken)
	}
	if !env.sp.IsRunning("worker") {
		t.Error("session should have been started via Provider")
	}
}

func TestReconcileSessionBeads_SkipsAliveSession(t *testing.T) {
	env := newReconcilerTestEnv()
	env.cfg = &config.City{Agents: []config.Agent{{Name: "worker"}}}
	env.addDesired("worker", "worker", true)
	session := env.createSessionBead("worker", "worker")

	woken := env.reconcile([]beads.Bead{session})

	if woken != 0 {
		t.Errorf("expected 0 woken, got %d", woken)
	}
}

func TestReconcileSessionBeads_SkipsQuarantinedSession(t *testing.T) {
	env := newReconcilerTestEnv()
	env.cfg = &config.City{Agents: []config.Agent{{Name: "worker"}}}
	env.addDesired("worker", "worker", false)
	session := env.createSessionBead("worker", "worker")
	// Set quarantine in the future.
	_ = env.store.SetMetadata(session.ID, "quarantined_until",
		env.clk.Now().Add(10*time.Minute).UTC().Format(time.RFC3339))
	session.Metadata["quarantined_until"] = env.clk.Now().Add(10 * time.Minute).UTC().Format(time.RFC3339)

	woken := env.reconcile([]beads.Bead{session})

	if woken != 0 {
		t.Errorf("expected 0 woken (quarantined), got %d", woken)
	}
}

func TestReconcileSessionBeads_RespectsWakeBudget(t *testing.T) {
	env := newReconcilerTestEnv()
	var cfgAgents []config.Agent
	var sessions []beads.Bead
	for i := 0; i < defaultMaxWakesPerTick+3; i++ {
		name := fmt.Sprintf("worker-%d", i)
		cfgAgents = append(cfgAgents, config.Agent{Name: name})
		env.addDesired(name, name, false)
		sessions = append(sessions, env.createSessionBead(name, name))
	}
	env.cfg = &config.City{Agents: cfgAgents}

	woken := env.reconcile(sessions)

	if woken != defaultMaxWakesPerTick {
		t.Errorf("expected %d woken (budget), got %d", defaultMaxWakesPerTick, woken)
	}
}

func TestReconcileSessionBeads_ConfigDriftInitiatesDrain(t *testing.T) {
	env := newReconcilerTestEnv()
	env.cfg = &config.City{Agents: []config.Agent{{Name: "worker"}}}
	// Desired state has a DIFFERENT config than what's in the bead.
	env.addDesiredWithConfig("worker", "worker", true, "new-cmd")
	session := env.createSessionBead("worker", "worker")

	// Verify hashes differ.
	storedHash := session.Metadata["config_hash"]
	currentHash := runtime.CoreFingerprint(runtime.Config{Command: "new-cmd"})
	if storedHash == currentHash {
		t.Fatalf("test setup error: stored hash %q should differ from current %q", storedHash, currentHash)
	}

	env.reconcile([]beads.Bead{session})

	ds := env.dt.get(session.ID)
	if ds == nil {
		t.Fatalf("expected drain to be initiated for config drift (session.ID=%q, stderr=%s)", session.ID, env.stderr.String())
	}
	if ds.reason != "config-drift" {
		t.Errorf("drain reason = %q, want %q", ds.reason, "config-drift")
	}
}

func TestReconcileSessionBeads_NoDriftWhenHashMatches(t *testing.T) {
	env := newReconcilerTestEnv()
	env.cfg = &config.City{Agents: []config.Agent{{Name: "worker"}}}
	env.addDesired("worker", "worker", true) // same config as bead
	session := env.createSessionBead("worker", "worker")

	env.reconcile([]beads.Bead{session})

	if ds := env.dt.get(session.ID); ds != nil {
		t.Errorf("expected no drain, got %+v", ds)
	}
}

func TestReconcileSessionBeads_DependencyOrdering_DepDeadBlocksWake(t *testing.T) {
	env := newReconcilerTestEnv()
	env.cfg = &config.City{
		Agents: []config.Agent{
			{Name: "worker", DependsOn: []string{"db"}},
			{Name: "db"},
		},
	}
	env.addDesired("worker", "worker", false)
	// db is in desired but starts fail (provider Start returns error).
	env.addDesired("db", "db", false)
	env.sp.StartErrors = map[string]error{"db": fmt.Errorf("db failed to start")}

	dbBead := env.createSessionBead("db", "db")
	workerBead := env.createSessionBead("worker", "worker")

	env.reconcile([]beads.Bead{workerBead, dbBead})

	// worker should NOT be started because db is still dead.
	// (db start failed, so sp.IsRunning("db") is false)
	if env.sp.IsRunning("worker") {
		t.Error("worker should NOT have been started (dep not alive)")
	}
}

func TestReconcileSessionBeads_DependencyOrdering_TopoOrder(t *testing.T) {
	env := newReconcilerTestEnv()
	env.cfg = &config.City{
		Agents: []config.Agent{
			{Name: "worker", DependsOn: []string{"db"}},
			{Name: "db"},
		},
	}
	env.addDesired("worker", "worker", false)
	env.addDesired("db", "db", false)

	dbBead := env.createSessionBead("db", "db")
	workerBead := env.createSessionBead("worker", "worker")

	// Even though worker bead is listed first, topo ordering ensures
	// db is processed first. Since the Fake provider marks sessions as
	// running on Start, worker can wake in the same tick after db succeeds.
	woken := env.reconcile([]beads.Bead{workerBead, dbBead})

	if woken != 2 {
		t.Errorf("expected 2 woken (both), got %d", woken)
	}
}

func TestReconcileSessionBeads_PoolDependencyBlocksWake(t *testing.T) {
	env := newReconcilerTestEnv()
	env.cfg = &config.City{
		Agents: []config.Agent{
			{Name: "worker", DependsOn: []string{"db"}},
			{Name: "db", Pool: &config.PoolConfig{Min: 2, Max: 2}},
		},
	}
	// Worker depends on pool "db". No db instances in desired → worker blocked.
	env.addDesired("worker", "worker", false)
	workerBead := env.createSessionBead("worker", "worker")

	woken := env.reconcile([]beads.Bead{workerBead})

	if woken != 0 {
		t.Errorf("expected 0 woken (pool dep dead), got %d", woken)
	}
}

func TestReconcileSessionBeads_PoolDependencyUnblocksWake(t *testing.T) {
	env := newReconcilerTestEnv()
	env.cfg = &config.City{
		Agents: []config.Agent{
			{Name: "worker", DependsOn: []string{"db"}},
			{Name: "db", Pool: &config.PoolConfig{Min: 2, Max: 2}},
		},
	}
	env.addDesired("worker", "worker", false)
	env.addDesired("db-1", "db", true) // one pool instance alive
	workerBead := env.createSessionBead("worker", "worker")

	woken := env.reconcile([]beads.Bead{workerBead})

	if woken != 1 {
		t.Errorf("expected 1 woken (pool dep alive), got %d", woken)
	}
}

func TestReconcileSessionBeads_OrphanSessionDrained(t *testing.T) {
	env := newReconcilerTestEnv()
	env.cfg = &config.City{Agents: []config.Agent{{Name: "other"}}}
	// Session bead for "orphan" with no matching desired entry, but running.
	_ = env.sp.Start(context.Background(), "orphan", runtime.Config{})
	session := env.createSessionBead("orphan", "orphan")

	env.reconcile([]beads.Bead{session})

	ds := env.dt.get(session.ID)
	if ds == nil {
		t.Fatal("expected drain for orphan session")
	}
	if ds.reason != "orphaned" {
		t.Errorf("drain reason = %q, want %q", ds.reason, "orphaned")
	}
}

func TestReconcileSessionBeads_OrphanNotRunningClosed(t *testing.T) {
	env := newReconcilerTestEnv()
	env.cfg = &config.City{Agents: []config.Agent{{Name: "other"}}}
	session := env.createSessionBead("orphan", "orphan")

	env.reconcile([]beads.Bead{session})

	b, _ := env.store.Get(session.ID)
	if b.Status != "closed" {
		t.Errorf("orphan bead status = %q, want closed", b.Status)
	}
	if b.Metadata["close_reason"] != "orphaned" {
		t.Errorf("close_reason = %q, want %q", b.Metadata["close_reason"], "orphaned")
	}
}

func TestReconcileSessionBeads_SuspendedSessionDrained(t *testing.T) {
	env := newReconcilerTestEnv()
	env.cfg = &config.City{Agents: []config.Agent{{Name: "worker"}}}
	// "worker" is in config (configuredNames) but NOT in desiredState.
	_ = env.sp.Start(context.Background(), "worker", runtime.Config{})
	session := env.createSessionBead("worker", "worker")

	env.reconcile([]beads.Bead{session})

	ds := env.dt.get(session.ID)
	if ds == nil {
		t.Fatal("expected drain for suspended session")
	}
	if ds.reason != "suspended" {
		t.Errorf("drain reason = %q, want %q", ds.reason, "suspended")
	}
}

func TestReconcileSessionBeads_SuspendedNotRunningClosed(t *testing.T) {
	env := newReconcilerTestEnv()
	env.cfg = &config.City{Agents: []config.Agent{{Name: "worker"}}}
	session := env.createSessionBead("worker", "worker")

	env.reconcile([]beads.Bead{session})

	b, _ := env.store.Get(session.ID)
	if b.Status != "closed" {
		t.Errorf("suspended bead status = %q, want closed", b.Status)
	}
	if b.Metadata["close_reason"] != "suspended" {
		t.Errorf("close_reason = %q, want %q", b.Metadata["close_reason"], "suspended")
	}
}

func TestReconcileSessionBeads_HealsExpiredTimers(t *testing.T) {
	env := newReconcilerTestEnv()
	env.cfg = &config.City{Agents: []config.Agent{{Name: "worker"}}}
	env.addDesired("worker", "worker", false)
	session := env.createSessionBead("worker", "worker")
	past := env.clk.Now().Add(-5 * time.Minute).UTC().Format(time.RFC3339)
	_ = env.store.SetMetadata(session.ID, "held_until", past)
	_ = env.store.SetMetadata(session.ID, "sleep_reason", "user-hold")
	session.Metadata["held_until"] = past
	session.Metadata["sleep_reason"] = "user-hold"

	env.reconcile([]beads.Bead{session})

	b, _ := env.store.Get(session.ID)
	if b.Metadata["held_until"] != "" {
		t.Error("expired held_until should be cleared")
	}
	if b.Metadata["sleep_reason"] != "" {
		t.Error("sleep_reason should be cleared with expired hold")
	}
}

func TestReconcileSessionBeads_CrashDetection(t *testing.T) {
	env := newReconcilerTestEnv()
	env.cfg = &config.City{Agents: []config.Agent{{Name: "worker"}}}
	env.addDesired("worker", "worker", false)
	session := env.createSessionBead("worker", "worker")
	recentWake := env.clk.Now().Add(-5 * time.Second).UTC().Format(time.RFC3339)
	_ = env.store.SetMetadata(session.ID, "last_woke_at", recentWake)
	session.Metadata["last_woke_at"] = recentWake

	env.reconcile([]beads.Bead{session})

	b, _ := env.store.Get(session.ID)
	if b.Metadata["wake_attempts"] != "1" {
		t.Errorf("wake_attempts = %q, want %q", b.Metadata["wake_attempts"], "1")
	}
}

func TestReconcileSessionBeads_StableClearsFailures(t *testing.T) {
	env := newReconcilerTestEnv()
	env.cfg = &config.City{Agents: []config.Agent{{Name: "worker"}}}
	env.addDesired("worker", "worker", true)
	session := env.createSessionBead("worker", "worker")
	stableWake := env.clk.Now().Add(-2 * time.Minute).UTC().Format(time.RFC3339)
	_ = env.store.SetMetadata(session.ID, "wake_attempts", "3")
	_ = env.store.SetMetadata(session.ID, "last_woke_at", stableWake)
	session.Metadata["wake_attempts"] = "3"
	session.Metadata["last_woke_at"] = stableWake

	env.reconcile([]beads.Bead{session})

	b, _ := env.store.Get(session.ID)
	if b.Metadata["wake_attempts"] != "0" {
		t.Errorf("wake_attempts = %q, want %q", b.Metadata["wake_attempts"], "0")
	}
}

func TestReconcileSessionBeads_NoAgentNotWoken(t *testing.T) {
	env := newReconcilerTestEnv()
	env.cfg = &config.City{}
	session := env.createSessionBead("orphan", "orphan")

	woken := env.reconcile([]beads.Bead{session})
	if woken != 0 {
		t.Errorf("expected 0 woken for orphan, got %d", woken)
	}
}

func TestReconcileSessionBeads_PreWakeCommitWritesMetadata(t *testing.T) {
	env := newReconcilerTestEnv()
	env.cfg = &config.City{Agents: []config.Agent{{Name: "worker"}}}
	env.addDesired("worker", "worker", false)
	session := env.createSessionBead("worker", "worker")

	env.reconcile([]beads.Bead{session})

	b, _ := env.store.Get(session.ID)
	if b.Metadata["generation"] != "2" {
		t.Errorf("generation = %q, want %q (incremented by preWakeCommit)", b.Metadata["generation"], "2")
	}
	if b.Metadata["instance_token"] == "test-token" {
		t.Error("instance_token should have been regenerated by preWakeCommit")
	}
	if b.Metadata["last_woke_at"] == "" {
		t.Error("last_woke_at should be set by preWakeCommit")
	}
}

func TestReconcileSessionBeads_CancelsDrainOnWakeReason(t *testing.T) {
	env := newReconcilerTestEnv()
	env.cfg = &config.City{Agents: []config.Agent{{Name: "worker"}}}
	env.addDesired("worker", "worker", true)
	session := env.createSessionBead("worker", "worker")

	gen := 1
	env.dt.set(session.ID, &drainState{
		startedAt:  env.clk.Now(),
		deadline:   env.clk.Now().Add(5 * time.Minute),
		reason:     "pool-excess",
		generation: gen,
	})

	env.reconcile([]beads.Bead{session})

	if ds := env.dt.get(session.ID); ds != nil {
		t.Errorf("drain should be canceled, got %+v", ds)
	}
}

func TestReconcileSessionBeads_UsesSleepIntentForDrainReason(t *testing.T) {
	env := newReconcilerTestEnv()
	env.cfg = &config.City{}
	env.addDesired("worker", "worker", true)
	_ = env.sp.Start(context.Background(), "worker", runtime.Config{})
	session := env.createSessionBead("worker", "worker")
	_ = env.store.SetMetadata(session.ID, "sleep_intent", "wait-hold")
	session.Metadata["sleep_intent"] = "wait-hold"

	env.reconcile([]beads.Bead{session})

	ds := env.dt.get(session.ID)
	if ds == nil {
		t.Fatal("expected drain when desired session has no wake reason")
	}
	if ds.reason != "wait-hold" {
		t.Fatalf("drain reason = %q, want wait-hold", ds.reason)
	}
}

func TestReconcileSessionBeads_StartFailureNoDoubleCounting(t *testing.T) {
	env := newReconcilerTestEnv()
	env.cfg = &config.City{Agents: []config.Agent{{Name: "worker"}}}
	env.addDesired("worker", "worker", false)
	env.sp.StartErrors = map[string]error{"worker": fmt.Errorf("start failed")}
	session := env.createSessionBead("worker", "worker")

	// First tick: Start fails, wake_attempts should be 1.
	env.reconcile([]beads.Bead{session})
	b, _ := env.store.Get(session.ID)
	if b.Metadata["wake_attempts"] != "1" {
		t.Fatalf("after first tick: wake_attempts = %q, want 1", b.Metadata["wake_attempts"])
	}

	// Second tick: reload bead from store to get updated metadata.
	b, _ = env.store.Get(session.ID)
	env.reconcile([]beads.Bead{b})
	b, _ = env.store.Get(session.ID)
	if b.Metadata["wake_attempts"] != "2" {
		t.Errorf("after second tick: wake_attempts = %q, want 2", b.Metadata["wake_attempts"])
	}
}

func TestReconcileSessionBeads_PoolScaleDownOrphansExcess(t *testing.T) {
	env := newReconcilerTestEnv()
	env.cfg = &config.City{
		Agents: []config.Agent{
			{Name: "worker", Pool: &config.PoolConfig{Min: 1, Max: 5}},
		},
	}
	// worker-1 is in the desired set; worker-2 is NOT (scale-down).
	env.addDesired("worker-1", "worker", true)
	// worker-2 is running in provider but not in desiredState.
	_ = env.sp.Start(context.Background(), "worker-2", runtime.Config{})
	s1 := env.createSessionBead("worker-1", "worker")
	_ = env.store.SetMetadata(s1.ID, "pool_slot", "1")
	s1.Metadata["pool_slot"] = "1"
	s2 := env.createSessionBead("worker-2", "worker")
	_ = env.store.SetMetadata(s2.ID, "pool_slot", "2")
	s2.Metadata["pool_slot"] = "2"

	poolDesired := map[string]int{"worker": 1}
	cfgNames := configuredSessionNames(env.cfg, "", env.store)
	reconcileSessionBeads(
		context.Background(), []beads.Bead{s1, s2}, env.desiredState, cfgNames,
		env.cfg, env.sp, env.store, nil, nil, env.dt, poolDesired, "",
		env.clk, env.rec, 0, 0, &env.stdout, &env.stderr,
	)

	d2 := env.dt.get(s2.ID)
	if d2 == nil {
		t.Fatal("expected drain for excess pool instance")
	}
	if d2.reason != "orphaned" {
		t.Errorf("drain reason = %q, want %q", d2.reason, "orphaned")
	}
	if d1 := env.dt.get(s1.ID); d1 != nil {
		t.Errorf("worker-1 should not be draining, got reason=%q", d1.reason)
	}
}

func TestReconcileSessionBeads_LiveDriftReapplied(t *testing.T) {
	env := newReconcilerTestEnv()
	env.cfg = &config.City{Agents: []config.Agent{{Name: "worker"}}}
	// Same core config (test-cmd), different live config.
	env.addDesiredLive("worker", "worker", true, []string{"echo live-updated"})
	session := env.createSessionBead("worker", "worker")

	env.reconcile([]beads.Bead{session})

	// Should NOT drain (core hash matches), but live_hash should be updated.
	if ds := env.dt.get(session.ID); ds != nil {
		t.Errorf("expected no drain for live-only drift, got reason=%q", ds.reason)
	}
	b, _ := env.store.Get(session.ID)
	expectedCfg := templateParamsToConfig(env.desiredState["worker"])
	expectedLive := runtime.LiveFingerprint(expectedCfg)
	if b.Metadata["live_hash"] != expectedLive {
		t.Errorf("live_hash not updated: got %q, want %q", b.Metadata["live_hash"], expectedLive)
	}
}

func TestAllDependenciesAlive_WithSessionTemplate(t *testing.T) {
	session := beads.Bead{Metadata: map[string]string{"template": "worker"}}
	cfg := &config.City{
		Workspace: config.Workspace{SessionTemplate: "{{.City}}-{{.Agent}}"},
		Agents: []config.Agent{
			{Name: "worker", DependsOn: []string{"db"}},
			{Name: "db"},
		},
	}
	sn := agent.SessionNameFor("myCity", "db", "{{.City}}-{{.Agent}}")
	sp := runtime.NewFake()
	_ = sp.Start(context.Background(), sn, runtime.Config{})
	desired := map[string]TemplateParams{
		sn: {TemplateName: "db"},
	}
	if !allDependenciesAlive(session, cfg, desired, sp, "myCity", nil) {
		t.Errorf("dep should be alive (session name: %q)", sn)
	}
}

func TestReconcileSessionBeads_DriftDrainUsesConfigTimeout(t *testing.T) {
	env := newReconcilerTestEnv()
	env.cfg = &config.City{
		Agents: []config.Agent{{Name: "worker"}},
		Daemon: config.DaemonConfig{DriftDrainTimeout: "7m"},
	}
	env.addDesiredWithConfig("worker", "worker", true, "new-cmd")
	session := env.createSessionBead("worker", "worker")

	cfgNames := configuredSessionNames(env.cfg, "", env.store)
	reconcileSessionBeads(
		context.Background(), []beads.Bead{session}, env.desiredState, cfgNames,
		env.cfg, env.sp, env.store, nil, nil, env.dt, map[string]int{}, "",
		env.clk, env.rec, 0, env.cfg.Daemon.DriftDrainTimeoutDuration(),
		&env.stdout, &env.stderr,
	)

	ds := env.dt.get(session.ID)
	if ds == nil {
		t.Fatal("expected drain for config drift")
	}
	expected := env.clk.Now().Add(7 * time.Minute)
	if ds.deadline != expected {
		t.Errorf("drain deadline = %v, want %v (7m from now)", ds.deadline, expected)
	}
}

// --- resolveAgentTemplate tests ---

func TestResolveAgentTemplate_DirectMatch(t *testing.T) {
	cfg := &config.City{Agents: []config.Agent{{Name: "overseer"}}}
	if got := resolveAgentTemplate("overseer", cfg); got != "overseer" {
		t.Errorf("got %q, want %q", got, "overseer")
	}
}

func TestResolveAgentTemplate_PoolInstance(t *testing.T) {
	cfg := &config.City{
		Agents: []config.Agent{
			{Name: "worker", Pool: &config.PoolConfig{Min: 1, Max: 5}},
		},
	}
	if got := resolveAgentTemplate("worker-3", cfg); got != "worker" {
		t.Errorf("got %q, want %q", got, "worker")
	}
}

func TestResolveAgentTemplate_Fallback(t *testing.T) {
	cfg := &config.City{}
	if got := resolveAgentTemplate("unknown", cfg); got != "unknown" {
		t.Errorf("got %q, want %q", got, "unknown")
	}
}

func TestResolveAgentTemplate_NilConfig(t *testing.T) {
	if got := resolveAgentTemplate("test", nil); got != "test" {
		t.Errorf("got %q, want %q", got, "test")
	}
}

// --- resolvePoolSlot tests ---

func TestResolvePoolSlot_PoolInstance(t *testing.T) {
	if got := resolvePoolSlot("worker-3", "worker"); got != 3 {
		t.Errorf("got %d, want 3", got)
	}
}

func TestResolvePoolSlot_NonPool(t *testing.T) {
	if got := resolvePoolSlot("overseer", "overseer"); got != 0 {
		t.Errorf("got %d, want 0", got)
	}
}

func TestResolvePoolSlot_NonNumericSuffix(t *testing.T) {
	if got := resolvePoolSlot("worker-abc", "worker"); got != 0 {
		t.Errorf("got %d, want 0", got)
	}
}

func TestResolveResumeCommand(t *testing.T) {
	tests := []struct {
		name       string
		command    string
		sessionKey string
		provider   *config.ResolvedProvider
		want       string
	}{
		{
			name:       "no resume flag → unchanged",
			command:    "claude --dangerously-skip-permissions",
			sessionKey: "abc-123",
			provider:   &config.ResolvedProvider{},
			want:       "claude --dangerously-skip-permissions",
		},
		{
			name:       "flag style",
			command:    "claude --dangerously-skip-permissions",
			sessionKey: "abc-123",
			provider:   &config.ResolvedProvider{ResumeFlag: "--resume"},
			want:       "claude --dangerously-skip-permissions --resume abc-123",
		},
		{
			name:       "subcommand style",
			command:    "codex --model o3",
			sessionKey: "def-456",
			provider:   &config.ResolvedProvider{ResumeFlag: "resume", ResumeStyle: "subcommand"},
			want:       "codex resume def-456 --model o3",
		},
		{
			name:       "subcommand style no args",
			command:    "codex",
			sessionKey: "def-456",
			provider:   &config.ResolvedProvider{ResumeFlag: "resume", ResumeStyle: "subcommand"},
			want:       "codex resume def-456",
		},
		{
			name:       "explicit resume_command takes precedence",
			command:    "claude --dangerously-skip-permissions",
			sessionKey: "abc-123",
			provider: &config.ResolvedProvider{
				ResumeFlag:    "--resume",
				ResumeCommand: "claude --resume {{.SessionKey}} --dangerously-skip-permissions",
			},
			want: "claude --resume abc-123 --dangerously-skip-permissions",
		},
		{
			name:       "resume_command without SessionKey placeholder",
			command:    "my-agent",
			sessionKey: "xyz",
			provider: &config.ResolvedProvider{
				ResumeCommand: "my-agent --continue",
			},
			want: "my-agent --continue",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveResumeCommand(tt.command, tt.sessionKey, tt.provider)
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResolveSessionCommand(t *testing.T) {
	claude := &config.ResolvedProvider{
		ResumeFlag:    "--resume",
		SessionIDFlag: "--session-id",
	}

	t.Run("first start uses --session-id", func(t *testing.T) {
		got := resolveSessionCommand("claude --dangerously-skip-permissions", "abc-123", claude, true)
		want := "claude --dangerously-skip-permissions --session-id abc-123"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("resume uses --resume", func(t *testing.T) {
		got := resolveSessionCommand("claude --dangerously-skip-permissions", "abc-123", claude, false)
		want := "claude --dangerously-skip-permissions --resume abc-123"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("first start without SessionIDFlag falls back to resume", func(t *testing.T) {
		noSessionID := &config.ResolvedProvider{ResumeFlag: "--resume"}
		got := resolveSessionCommand("agent run", "key-1", noSessionID, true)
		want := "agent run --resume key-1"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
}
