package mister

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ConfStrSidecarDir holds per-build CONF_STR files: <RBF name>.txt next to
// nothing else, e.g. MSX1_20261004d_opl4regrd.txt for
// _Computer/MSX1_20261004d_opl4regrd.rbf.  A core whose CONF_STR changes
// from build to build ships one with every RBF; the scraped database only
// knows one version of each core (var so tests can redirect it).
var ConfStrSidecarDir = "/media/fat/config/confstr"

// ResolvedOSD is a core's OSD description and where it came from.
type ResolvedOSD struct {
	OSD *CoreOSD
	// "sidecar" (exact file for this RBF), "database" (exact name match) or
	// "database-fuzzy" (only for read-only callers).
	Source string
	Path   string // sidecar file, when Source == "sidecar"
}

func loadSidecar(path string) (*CoreOSD, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	raw := strings.TrimSpace(string(b))
	if raw == "" {
		return nil, fmt.Errorf("empty CONF_STR file %s", path)
	}
	return &CoreOSD{
		CoreName:   ExtractCoreName(raw),
		RbfName:    strings.TrimSuffix(filepath.Base(path), ".txt"),
		Repo:       "sidecar:" + path,
		ConfStrRaw: raw,
		Menu:       ParseConfStr(raw),
	}, nil
}

// sidecarBuilds lists the RBF names that have a sidecar whose CONF_STR
// belongs to the core named confStrName.
func sidecarBuilds(confStrName string) []string {
	files, _ := filepath.Glob(filepath.Join(ConfStrSidecarDir, "*.txt"))
	var out []string
	for _, f := range files {
		osd, err := loadSidecar(f)
		if err == nil && strings.EqualFold(osd.CoreName, confStrName) {
			out = append(out, osd.RbfName)
		}
	}
	return out
}

// LookupCoreOSDExact matches core_name or rbf_name exactly (ignoring case),
// never by similarity.
func LookupCoreOSDExact(db *ConfStrDB, name string) *CoreOSD {
	for i := range db.Cores {
		if strings.EqualFold(db.Cores[i].CoreName, name) {
			return &db.Cores[i]
		}
	}
	for i := range db.Cores {
		if db.Cores[i].RbfName != "" && strings.EqualFold(db.Cores[i].RbfName, name) {
			return &db.Cores[i]
		}
	}
	return nil
}

func lookupDB(db *ConfStrDB, name string, strict bool) (*ResolvedOSD, error) {
	if osd := LookupCoreOSDExact(db, name); osd != nil {
		return &ResolvedOSD{OSD: osd, Source: "database"}, nil
	}
	if strict {
		return nil, fmt.Errorf("no exact CONF_STR entry for core %q (similar names are not used for writes or OSD navigation)", name)
	}
	if osd := LookupCoreOSD(db, name); osd != nil {
		return &ResolvedOSD{OSD: osd, Source: "database-fuzzy"}, nil
	}
	return nil, fmt.Errorf("no OSD info found for core: %s", name)
}

// ResolveRunningOSD finds the OSD description of the running core.  A
// sidecar for exactly this RBF wins.  If the core ships sidecars but none
// for this build, the database entry describes some other build and is
// refused.  strict (writes, OSD navigation) allows exact names only.
func ResolveRunningOSD(db *ConfStrDB, st *CoreStatus, strict bool) (*ResolvedOSD, error) {
	if st.CoreName != "" {
		p := filepath.Join(ConfStrSidecarDir, st.CoreName+".txt")
		if osd, err := loadSidecar(p); err == nil {
			return &ResolvedOSD{OSD: osd, Source: "sidecar", Path: p}, nil
		}
	}
	name := st.LookupName()
	if builds := sidecarBuilds(name); len(builds) > 0 {
		return nil, fmt.Errorf("core %s has per-build CONF_STR files in %s (%s) but none for %s; refusing to guess its OSD layout",
			name, ConfStrSidecarDir, strings.Join(builds, ", "), st.CoreName)
	}
	return lookupDB(db, name, strict)
}

// ResolveNamedOSD looks a core up by a name the caller gave.  A core that
// ships per-build sidecars cannot be resolved this way: the name does not
// say which build is meant.
func ResolveNamedOSD(db *ConfStrDB, name string, strict bool) (*ResolvedOSD, error) {
	if builds := sidecarBuilds(name); len(builds) > 0 {
		return nil, fmt.Errorf("core %s has per-build CONF_STR files (%s); run it and omit the core name so the running build is used",
			name, strings.Join(builds, ", "))
	}
	return lookupDB(db, name, strict)
}
