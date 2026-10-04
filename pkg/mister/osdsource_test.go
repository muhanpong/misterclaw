package mister

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sidecarTestDB() *ConfStrDB {
	return &ConfStrDB{Cores: []CoreOSD{
		{CoreName: "MSX1", Repo: "MiSTer-devel/MSX1_MiSTer", ConfStrRaw: "MSX1;;O1,Upstream Opt,A,B"},
		{CoreName: "MSX", Repo: "MiSTer-devel/MSX_MiSTer", ConfStrRaw: "MSX;;O2,Other core,A,B"},
		{CoreName: "PC8801", Repo: "MiSTer-devel/PC88_MiSTer", ConfStrRaw: "PC8801;;O3,PC opt,A,B"},
	}}
}

func withSidecarDir(t *testing.T) string {
	dir := t.TempDir()
	old := ConfStrSidecarDir
	ConfStrSidecarDir = dir
	t.Cleanup(func() { ConfStrSidecarDir = old })
	return dir
}

func TestResolveOSD_SidecarForThisBuild(t *testing.T) {
	dir := withSidecarDir(t)
	os.WriteFile(filepath.Join(dir, "MSX1_20261004d_opl4regrd.txt"), []byte("MSX1;;O[45],MoonSound,Off,On\n"), 0644)
	st := &CoreStatus{CoreName: "MSX1_20261004d_opl4regrd", ConfStrName: "MSX1"}
	r, err := ResolveRunningOSD(sidecarTestDB(), st, true)
	if err != nil {
		t.Fatal(err)
	}
	if r.Source != "sidecar" || !strings.Contains(r.OSD.ConfStrRaw, "MoonSound") || r.OSD.CoreName != "MSX1" {
		t.Errorf("got source=%s core=%s raw=%q", r.Source, r.OSD.CoreName, r.OSD.ConfStrRaw)
	}
}

func TestResolveOSD_RefusesStaleDBWhenCoreShipsSidecars(t *testing.T) {
	// A sidecar exists for another build of MSX1 but not this one: the
	// database entry (upstream MSX1, another bit layout) must not be used.
	dir := withSidecarDir(t)
	os.WriteFile(filepath.Join(dir, "MSX1_20260927c_mupack.txt"), []byte("MSX1;;O[45],MoonSound,Off,On"), 0644)
	st := &CoreStatus{CoreName: "MSX1_20261004d_opl4regrd", ConfStrName: "MSX1"}
	for _, strict := range []bool{true, false} {
		if _, err := ResolveRunningOSD(sidecarTestDB(), st, strict); err == nil || !strings.Contains(err.Error(), "MSX1_20261004d_opl4regrd") {
			t.Errorf("strict=%v: want refusal naming the build, got %v", strict, err)
		}
	}
}

func TestResolveOSD_StrictIsExactOnly(t *testing.T) {
	withSidecarDir(t)
	// "PC88" only reaches "PC8801" by fuzzy matching.
	st := &CoreStatus{CoreName: "PC88_20250918"}
	if _, err := ResolveRunningOSD(sidecarTestDB(), st, true); err == nil {
		t.Error("strict lookup accepted a fuzzy match")
	}
	r, err := ResolveRunningOSD(sidecarTestDB(), st, false)
	if err != nil || r.OSD.CoreName != "PC8801" || r.Source != "database-fuzzy" {
		t.Errorf("non-strict: got %+v, %v", r, err)
	}
	st = &CoreStatus{CoreName: "MSX1_20240101", ConfStrName: "MSX1"}
	if r, err := ResolveRunningOSD(sidecarTestDB(), st, true); err != nil || r.Source != "database" || r.OSD.CoreName != "MSX1" {
		t.Errorf("exact: got %+v, %v", r, err)
	}
}

func TestResolveNamedOSD_RefusesCoreWithSidecars(t *testing.T) {
	// core="MSX1" given by name cannot say which build is meant.
	dir := withSidecarDir(t)
	os.WriteFile(filepath.Join(dir, "MSX1_20260927c_mupack.txt"), []byte("MSX1;;O[45],MoonSound,Off,On"), 0644)
	if _, err := ResolveNamedOSD(sidecarTestDB(), "MSX1", true); err == nil {
		t.Error("named lookup of a sidecar core was accepted")
	}
	if r, err := ResolveNamedOSD(sidecarTestDB(), "MSX", true); err != nil || r.OSD.CoreName != "MSX" {
		t.Errorf("unrelated core: %+v %v", r, err)
	}
}
