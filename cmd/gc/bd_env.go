package main

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/citylayout"
)

// bdCommandRunnerForCity centralizes bd subprocess env construction so all
// GC-managed bd calls resolve Dolt against the same city-scoped runtime.
// Sets BEADS_DIR to cityPath/.beads so bd uses the city's routing context
// regardless of what the parent process's BEADS_DIR is set to.
func bdCommandRunnerForCity(cityPath string) beads.CommandRunner {
	env := bdRuntimeEnv(cityPath)
	env["BEADS_DIR"] = filepath.Join(cityPath, ".beads")
	return beads.ExecCommandRunnerWithEnv(env)
}

// bdStoreForCity creates a BdStore rooted at dir using the city's Dolt
// connection. Sets BEADS_DIR to dir/.beads so bd uses dir's routing context
// for cross-rig lookups — rig .beads/ dirs determine the db/prefix only,
// not the server connection (which is pinned via BEADS_DOLT_PORT).
func bdStoreForCity(dir, cityPath string) *beads.BdStore {
	env := bdRuntimeEnv(cityPath)
	env["BEADS_DIR"] = filepath.Join(dir, ".beads")
	return beads.NewBdStore(dir, beads.ExecCommandRunnerWithEnv(env))
}

func bdStoreForDir(dir string) *beads.BdStore {
	return bdStoreForCity(dir, cityForStoreDir(dir))
}

// rigRunnerForCity creates a CommandRunner for a rig-scoped store. beadsDir is
// the rig's .beads/ directory; cityPath provides the Dolt connection parameters.
// This creates beads in the rig's database rather than the city database.
func rigRunnerForCity(beadsDir, cityPath string) beads.CommandRunner {
	env := bdRuntimeEnv(cityPath)
	env["BEADS_DIR"] = beadsDir
	return beads.ExecCommandRunnerWithEnv(env)
}

func bdRuntimeEnv(cityPath string) map[string]string {
	env := citylayout.CityRuntimeEnvMap(cityPath)
	if rawBeadsProvider(cityPath) != "bd" {
		return env
	}
	if port := currentDoltPort(cityPath); port != "" {
		env["GC_DOLT_PORT"] = port
		// BEADS_DOLT_PORT pins bd's Dolt connection for cross-rig routing:
		// when bd follows routes.jsonl to a rig dir, it must stay on the
		// central server instead of picking up a stale rig port file.
		env["BEADS_DOLT_PORT"] = port
		return env
	}
	// Best-effort recovery for managed cities: if state is stale or missing,
	// ask the provider to repair itself before bd falls back to auto-start.
	if err := healthBeadsProvider(cityPath); err == nil {
		if port := currentDoltPort(cityPath); port != "" {
			env["GC_DOLT_PORT"] = port
			env["BEADS_DOLT_PORT"] = port
		}
	}
	return env
}

func cityRuntimeProcessEnv(cityPath string) []string {
	overrides := citylayout.CityRuntimeEnvMap(cityPath)
	if rawBeadsProvider(cityPath) == "bd" {
		if port := currentDoltPort(cityPath); port != "" {
			overrides["GC_DOLT_PORT"] = port
		}
	}
	return mergeRuntimeEnv(os.Environ(), overrides)
}

func cityForStoreDir(dir string) string {
	if gcCity := os.Getenv("GC_CITY"); gcCity != "" {
		if p, err := findCity(gcCity); err == nil {
			return p
		}
	}
	if p, err := findCity(dir); err == nil {
		return p
	}
	return dir
}

func mergeRuntimeEnv(environ []string, overrides map[string]string) []string {
	keys := []string{
		"GC_CITY",
		"GC_CITY_ROOT",
		"GC_CITY_PATH",
		"GC_CITY_RUNTIME_DIR",
		"GC_DOLT_PORT",
		"GC_PACK_STATE_DIR",
	}
	if len(overrides) > 0 {
		for key := range overrides {
			if !containsString(keys, key) {
				keys = append(keys, key)
			}
		}
	}
	sort.Strings(keys)
	out := append([]string(nil), environ...)
	for _, key := range keys {
		out = removeEnvKey(out, key)
	}
	overrideKeys := make([]string, 0, len(overrides))
	for key := range overrides {
		overrideKeys = append(overrideKeys, key)
	}
	sort.Strings(overrideKeys)
	for _, key := range overrideKeys {
		out = append(out, key+"="+overrides[key])
	}
	return out
}

func removeEnvKey(environ []string, key string) []string {
	prefix := key + "="
	out := make([]string, 0, len(environ))
	for _, entry := range environ {
		if !strings.HasPrefix(entry, prefix) {
			out = append(out, entry)
		}
	}
	return out
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
