package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/events"
	"github.com/gastownhall/gascity/internal/fsys"
	"github.com/gastownhall/gascity/internal/runtime"
)

func TestCityRuntimeReloadProviderSwapPreservesDrainTracker(t *testing.T) {
	cityPath := t.TempDir()
	tomlPath := filepath.Join(cityPath, "city.toml")
	writeCityRuntimeConfig(t, tomlPath, "fake")

	cfg, err := config.Load(osFS{}, tomlPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	sp := runtime.NewFake()
	var stdout bytes.Buffer
	cr := newCityRuntime(CityRuntimeParams{
		CityPath: cityPath,
		CityName: "test-city",
		TomlPath: tomlPath,
		Cfg:      cfg,
		SP:       sp,
		BuildFn: func(*config.City, runtime.Provider, beads.Store) map[string]TemplateParams {
			return map[string]TemplateParams{}
		},
		Dops:   newDrainOps(sp),
		Rec:    events.Discard,
		Stdout: &stdout,
		Stderr: io.Discard,
	})

	cs := newControllerState(cfg, sp, events.NewFake(), "test-city", cityPath)
	cs.cityBeadStore = beads.NewMemStore()
	cr.setControllerState(cs)

	// Manually initialize drain tracker (normally done in run()).
	cr.sessionDrains = newDrainTracker()

	writeCityRuntimeConfig(t, tomlPath, "fail")
	lastProviderName := "fake"
	cr.reloadConfig(context.Background(), &lastProviderName, cityPath)

	if lastProviderName != "fail" {
		t.Fatalf("lastProviderName = %q, want fail", lastProviderName)
	}
	if cr.sessionDrains == nil {
		t.Fatal("sessionDrains = nil after provider swap, want non-nil")
	}
}

func TestCityRuntimeReloadSameRevisionIsNoOp(t *testing.T) {
	cityPath := t.TempDir()
	tomlPath := filepath.Join(cityPath, "city.toml")
	writeCityRuntimeConfig(t, tomlPath, "fake")

	cfg, prov, err := config.LoadWithIncludes(fsys.OSFS{}, tomlPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	configRev := config.Revision(fsys.OSFS{}, prov, cfg, cityPath)

	sp := runtime.NewFake()
	var stdout bytes.Buffer
	cr := newCityRuntime(CityRuntimeParams{
		CityPath:  cityPath,
		CityName:  "test-city",
		TomlPath:  tomlPath,
		ConfigRev: configRev,
		Cfg:       cfg,
		SP:        sp,
		BuildFn: func(*config.City, runtime.Provider, beads.Store) map[string]TemplateParams {
			return map[string]TemplateParams{}
		},
		Dops:   newDrainOps(sp),
		Rec:    events.Discard,
		Stdout: &stdout,
		Stderr: io.Discard,
	})

	oldCfg := cr.cfg
	lastProviderName := "fake"
	cr.reloadConfig(context.Background(), &lastProviderName, cityPath)

	if cr.cfg != oldCfg {
		t.Fatal("same-revision reload should keep existing config pointer")
	}
	if cr.configRev != configRev {
		t.Fatalf("configRev = %q, want %q", cr.configRev, configRev)
	}
	if lastProviderName != "fake" {
		t.Fatalf("lastProviderName = %q, want fake", lastProviderName)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty for same-revision reload", stdout.String())
	}
}

func writeCityRuntimeConfig(t *testing.T, tomlPath, provider string) {
	t.Helper()
	data := []byte("[workspace]\nname = \"test-city\"\n\n[beads]\nprovider = \"file\"\n\n[session]\nprovider = \"" + provider + "\"\n")
	if err := os.WriteFile(tomlPath, data, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
}
