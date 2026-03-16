package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/events"
	"github.com/gastownhall/gascity/internal/runtime"
)

func TestCityRuntimeReloadProviderSwapRestoresBeadReconcileOps(t *testing.T) {
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
	cr.upgradeToBeadReconcileOps()
	if _, ok := cr.rops.(*beadReconcileOps); !ok {
		t.Fatalf("initial rops = %T, want *beadReconcileOps", cr.rops)
	}

	writeCityRuntimeConfig(t, tomlPath, "fail")
	lastProviderName := "fake"
	cr.reloadConfig(context.Background(), &lastProviderName, cityPath)

	if lastProviderName != "fail" {
		t.Fatalf("lastProviderName = %q, want fail", lastProviderName)
	}
	if _, ok := cr.rops.(*beadReconcileOps); !ok {
		t.Fatalf("rops after provider swap = %T, want *beadReconcileOps", cr.rops)
	}
	if cr.sessionDrains == nil {
		t.Fatal("sessionDrains = nil, want default-on bead reconciler to enable drain tracker")
	}
	if !strings.Contains(stdout.String(), "set daemon.bead_reconciler=false to use legacy") {
		t.Fatalf("stdout = %q, want default-on bead_reconciler note", stdout.String())
	}
}

func writeCityRuntimeConfig(t *testing.T, tomlPath, provider string) {
	t.Helper()
	data := []byte("[workspace]\nname = \"test-city\"\n\n[beads]\nprovider = \"file\"\n\n[session]\nprovider = \"" + provider + "\"\n")
	if err := os.WriteFile(tomlPath, data, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
}
