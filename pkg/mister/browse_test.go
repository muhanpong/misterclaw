package mister

import (
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// fakeBrowser models MiSTer's file browser as seen through the
// log_file_entry files: ".." first (except at the root), entries sorted,
// typing filters to entries starting with the text, Enter enters a
// directory or selects a file.
type fakeBrowser struct {
	tree   map[string][]string // dir -> entries; directories end in "/"
	root   string
	dir    string
	filter string
	cur    int
	sel    string
	keys   []string
	recent bool   // typed within MiSTer's 2 s filter window
	acc    string // MiSTer's filter text: survives entering a directory
}

func (b *fakeBrowser) entries() []string {
	var out []string
	if b.dir != b.root {
		out = append(out, "..")
	}
	es := append([]string(nil), b.tree[b.dir]...)
	sort.Strings(es)
	for _, e := range es {
		n := strings.TrimSuffix(e, "/")
		if b.filter == "" || strings.Contains(strings.ToLower(n), b.filter) {
			out = append(out, n)
		}
	}
	return out
}

func (b *fakeBrowser) isDir(n string) bool {
	for _, e := range b.tree[b.dir] {
		if e == n+"/" {
			return true
		}
	}
	return false
}

func (b *fakeBrowser) io() browserIO {
	return browserIO{
		press: func(k string) error {
			b.keys = append(b.keys, k)
			es := b.entries()
			switch k {
			case "home":
				b.cur = 0
			case "down":
				if b.cur < len(es)-1 {
					b.cur++
				}
			case "esc":
				b.sel = "cancelled"
			case "enter":
				if len(es) == 0 {
					return nil
				}
				n := es[b.cur]
				switch {
				case n == "..":
					b.dir = filepath.Dir(b.dir)
				case b.isDir(n):
					b.dir = filepath.Join(b.dir, n)
				default:
					b.sel = "selected:" + filepath.Join(b.dir, n)
					return nil
				}
				b.filter, b.cur = "", 0
			}
			return nil
		},
		typ: func(t string) error {
			b.keys = append(b.keys, "type:"+t)
			if b.recent {
				b.acc += t // within 2 s: appended, as MiSTer does
			} else {
				b.acc = t
			}
			b.filter = b.acc
			b.recent, b.cur = true, 0
			return nil
		},
		filterGap: func() { b.recent = false },
		state: func() BrowserState {
			es := b.entries()
			item := ""
			if b.cur < len(es) {
				item = es[b.cur]
			}
			sel := "active"
			if strings.HasPrefix(b.sel, "selected") {
				sel = "selected"
			} else if b.sel != "" {
				sel = b.sel
			}
			return BrowserState{Select: sel, Dir: b.dir, Item: item}
		},
		wait: func() {},
	}
}

func TestBrowse_FilterWindowNotExtended(t *testing.T) {
	// Without waiting out the 2 s window, "dsks" + "msxtools" became one
	// filter on the board; the walk must start each name afresh.
	b := newFakeBrowser("/media/fat/games/MSX1")
	b.tree["/media/fat/games/MSX1/DSKS"] = append(b.tree["/media/fat/games/MSX1/DSKS"], "MSXTOOLS.DSK")
	b.tree["/media/fat/games/MSX1"] = append(b.tree["/media/fat/games/MSX1"], "GoodMSX1/")
	if _, err := b.io().browseAndSelect("/media/fat/games/MSX1/DSKS/MSXTOOLS.DSK"); err != nil {
		t.Fatalf("%v keys=%v", err, b.keys)
	}
}

func newFakeBrowser(start string) *fakeBrowser {
	return &fakeBrowser{
		root: "/media/fat",
		dir:  start,
		tree: map[string][]string{
			"/media/fat":                          {"games/"},
			"/media/fat/games":                    {"MSX1/", "SNES/"},
			"/media/fat/games/MSX1":               {"DSKS/", "MSX/", "msxsd_2x2GB_4x4GB.vhd", "Ranma 1-2 (1-8)(k)(fix).dsk"},
			"/media/fat/games/MSX1/DSKS":          {"ASO_WAIT/", "Disk1.dsk", "Disk10.dsk", "disk2.dsk"},
			"/media/fat/games/MSX1/MSX":           {"Panasonic/"},
			"/media/fat/games/MSX1/DSKS/ASO_WAIT": {"a_wait07.DSK"},
		},
	}
}

func TestBrowse_SameDirExactName(t *testing.T) {
	b := newFakeBrowser("/media/fat/games/MSX1/DSKS")
	st, err := b.io().browseAndSelect("/media/fat/games/MSX1/DSKS/Disk1.dsk")
	if err != nil || b.sel != "selected:/media/fat/games/MSX1/DSKS/Disk1.dsk" {
		t.Fatalf("got %v %+v sel=%s keys=%v", err, st, b.sel, b.keys)
	}
}

func TestBrowse_UpThenDown(t *testing.T) {
	// From a sibling directory: up to MSX1, down into DSKS/ASO_WAIT.
	b := newFakeBrowser("/media/fat/games/MSX1/MSX")
	if _, err := b.io().browseAndSelect("/media/fat/games/MSX1/DSKS/ASO_WAIT/a_wait07.DSK"); err != nil {
		t.Fatalf("%v keys=%v", err, b.keys)
	}
	if b.sel != "selected:/media/fat/games/MSX1/DSKS/ASO_WAIT/a_wait07.DSK" {
		t.Errorf("selected %s", b.sel)
	}
}

func TestBrowse_FilterStopsAtSpecialCharThenWalks(t *testing.T) {
	// "Ranma 1-2 (1-8)..." -> filter "ranma" (no space: Space selects) then the exact name is
	// confirmed through CURRENTPATH before Enter.
	b := newFakeBrowser("/media/fat/games/MSX1")
	if _, err := b.io().browseAndSelect("/media/fat/games/MSX1/Ranma 1-2 (1-8)(k)(fix).dsk"); err != nil {
		t.Fatalf("%v keys=%v", err, b.keys)
	}
}

func TestBrowse_NeverTypesSpaceOrMinus(t *testing.T) {
	b := newFakeBrowser("/media/fat/games/MSX1")
	b.io().browseAndSelect("/media/fat/games/MSX1/Ranma 1-2 (1-8)(k)(fix).dsk")
	for _, k := range b.keys {
		if strings.HasPrefix(k, "type:") && strings.ContainsAny(k[5:], " -_") {
			t.Fatalf("typed Space or Minus (Select / previous-letter in the browser): %v", b.keys)
		}
	}
}

func TestBrowse_MissingFileCancels(t *testing.T) {
	b := newFakeBrowser("/media/fat/games/MSX1/DSKS")
	_, err := b.io().browseAndSelect("/media/fat/games/MSX1/DSKS/Disk3.dsk")
	if err == nil || b.sel != "cancelled" {
		t.Fatalf("want error and Esc, got %v sel=%q", err, b.sel)
	}
	for _, k := range b.keys {
		if k == "enter" {
			t.Fatalf("pressed Enter on a wrong entry: %v", b.keys)
		}
	}
}

func TestBrowse_NotVisibleWithoutLogFileEntry(t *testing.T) {
	io := browserIO{
		press: func(string) error { return nil },
		typ:   func(string) error { return nil },
		state: func() BrowserState { return BrowserState{} },
		wait:  func() {},
	}
	_, err := io.browseAndSelect("/media/fat/games/MSX1/x.dsk")
	if err == nil || !strings.Contains(err.Error(), "log_file_entry") {
		t.Fatalf("want log_file_entry hint, got %v", err)
	}
}

func TestFilterPrefix(t *testing.T) {
	for in, want := range map[string]string{
		"Disk1.dsk":                   "disk1",
		"Ranma 1-2 (1-8)(k)(fix).dsk": "ranma",
		"(Hack) Foo.rom":              "",
	} {
		if got := filterPrefix(in); got != want {
			t.Errorf("filterPrefix(%q) = %q, want %q", in, got, want)
		}
	}
}
