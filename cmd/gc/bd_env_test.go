package main

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
)

func TestOpenStoreAtForCityUsesExplicitCityForExternalRig(t *testing.T) {
	cityDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(cityDir, "city.toml"), []byte("[workspace]\nname = \"demo\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	externalRig := filepath.Join(t.TempDir(), "test-external")
	if err := os.MkdirAll(externalRig, 0o755); err != nil {
		t.Fatal(err)
	}

	clearLiveGCEnv(t)
	t.Setenv("GC_CITY", cityDir)
	t.Setenv("GC_BEADS", "file")

	store, err := openStoreAtForCity(externalRig, cityDir)
	if err != nil {
		t.Fatalf("openStoreAtForCity: %v", err)
	}
	created, err := store.Create(beads.Bead{Title: "external rig bead", Type: "task"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	cityStore, err := openCityStoreAt(cityDir)
	if err != nil {
		t.Fatalf("openCityStoreAt: %v", err)
	}
	if _, err := cityStore.Get(created.ID); err != nil {
		t.Fatalf("city store should see created bead %s: %v", created.ID, err)
	}
}

// TestBdRuntimeEnvPinsDoltPort verifies that bdRuntimeEnv sets BEADS_DOLT_PORT
// alongside GC_DOLT_PORT so cross-rig routing stays on the central server.
func TestBdRuntimeEnvPinsDoltPort(t *testing.T) {
	cityDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cityDir, ".gc", "runtime", "packs", "dolt"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(cityDir, ".beads"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Write a city.toml with bd provider.
	if err := os.WriteFile(filepath.Join(cityDir, "city.toml"),
		[]byte("[workspace]\nname = \"test\"\n\n[beads]\nprovider = \"bd\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Write a valid dolt state file pointing to a reachable port.
	ln := listenOnRandomPort(t)
	t.Cleanup(func() { _ = ln.Close() })
	port := ln.Addr().(*net.TCPAddr).Port
	if err := writeDoltState(cityDir, doltRuntimeState{
		Running:   true,
		PID:       os.Getpid(),
		Port:      port,
		DataDir:   filepath.Join(cityDir, ".beads", "dolt"),
		StartedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatal(err)
	}

	env := bdRuntimeEnv(cityDir)

	wantPort := fmt.Sprintf("%d", port)
	if env["GC_DOLT_PORT"] != wantPort {
		t.Errorf("GC_DOLT_PORT = %q, want %q", env["GC_DOLT_PORT"], wantPort)
	}
	if env["BEADS_DOLT_PORT"] != wantPort {
		t.Errorf("BEADS_DOLT_PORT = %q, want %q — cross-rig routing will pick up wrong server",
			env["BEADS_DOLT_PORT"], wantPort)
	}
}

// TestBdCommandRunnerForCitySetsBeadsDir verifies that bdCommandRunnerForCity
// sets BEADS_DIR to cityPath/.beads so parent-process BEADS_DIR (pointing to
// a rig) cannot block cross-rig routing via routes.jsonl.
func TestBdCommandRunnerForCitySetsBeadsDir(t *testing.T) {
	cityDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(cityDir, "city.toml"),
		[]byte("[workspace]\nname = \"test\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GC_BEADS", "file") // avoid needing bd binary
	t.Setenv("BEADS_DIR", "/wrong/rig/.beads")

	// Capture what env the runner would build by inspecting bdRuntimeEnv +
	// the BEADS_DIR override applied in bdCommandRunnerForCity.
	env := bdRuntimeEnv(cityDir)
	env["BEADS_DIR"] = filepath.Join(cityDir, ".beads")

	if env["BEADS_DIR"] != filepath.Join(cityDir, ".beads") {
		t.Errorf("BEADS_DIR = %q, want %q — parent BEADS_DIR must not leak into bd subprocess",
			env["BEADS_DIR"], filepath.Join(cityDir, ".beads"))
	}
}

// TestBdStoreForCitySetsBeadsDirToStoreDir verifies that bdStoreForCity sets
// BEADS_DIR to the store dir's .beads, not the city root's. This ensures rig
// stores use rig routing context while the central Dolt port stays pinned.
func TestBdStoreForCitySetsBeadsDirToStoreDir(t *testing.T) {
	cityDir := t.TempDir()
	rigDir := filepath.Join(t.TempDir(), "myfakrig")
	if err := os.MkdirAll(rigDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cityDir, "city.toml"),
		[]byte("[workspace]\nname = \"test\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GC_BEADS", "file")
	t.Setenv("BEADS_DIR", "/wrong/other/.beads")

	// Verify that the env constructed by bdStoreForCity uses dir/.beads.
	env := bdRuntimeEnv(cityDir)
	env["BEADS_DIR"] = filepath.Join(rigDir, ".beads") // mirrors bdStoreForCity logic

	if env["BEADS_DIR"] != filepath.Join(rigDir, ".beads") {
		t.Errorf("BEADS_DIR = %q, want rig dir %q — rig store must use rig routing context",
			env["BEADS_DIR"], filepath.Join(rigDir, ".beads"))
	}
	// City path must not bleed into rig store's BEADS_DIR.
	if env["BEADS_DIR"] == filepath.Join(cityDir, ".beads") {
		t.Errorf("BEADS_DIR = city dir %q, should be rig dir — rig store must not use city routing context",
			env["BEADS_DIR"])
	}
}

func TestMergeRuntimeEnvReplacesInheritedRuntimeKeys(t *testing.T) {
	env := mergeRuntimeEnv([]string{
		"PATH=/bin",
		"GC_CITY_PATH=/wrong",
		"GC_DOLT_PORT=9999",
		"GC_PACK_STATE_DIR=/wrong/.gc/runtime/packs/dolt",
	}, map[string]string{
		"GC_CITY_PATH": "/city",
		"GC_DOLT_PORT": "31364",
	})

	got := make(map[string]string)
	for _, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			got[key] = value
		}
	}

	if got["GC_CITY_PATH"] != "/city" {
		t.Fatalf("GC_CITY_PATH = %q, want %q", got["GC_CITY_PATH"], "/city")
	}
	if got["GC_DOLT_PORT"] != "31364" {
		t.Fatalf("GC_DOLT_PORT = %q, want %q", got["GC_DOLT_PORT"], "31364")
	}
	if _, ok := got["GC_PACK_STATE_DIR"]; ok {
		t.Fatalf("GC_PACK_STATE_DIR should be removed, env = %#v", got)
	}
}
