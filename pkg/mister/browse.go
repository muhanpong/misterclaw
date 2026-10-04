package mister

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// MiSTer main's file browser publishes its state when MiSTer.ini has
// log_file_entry=1 (menu.cpp, MENU_FILE_SELECT1 and the select/cancel
// hook): FULLPATH = the directory shown, CURRENTPATH = the highlighted
// entry, FILESELECT = active / selected / cancelled.  That is the only way to
// see the browser from Linux -- the OSD is not in screenshots -- so every
// step below is checked against these files before the next key.
var (
	browserSelectFile  = "/tmp/FILESELECT"
	browserDirFile     = "/tmp/FULLPATH"
	browserCurrentFile = "/tmp/CURRENTPATH"
	browserRoot        = "/media/fat" // FULLPATH is relative to it
)

// BrowserState is one reading of the browser files.
type BrowserState struct {
	Select string `json:"select"` // active, selected, cancelled or ""
	Dir    string `json:"dir"`    // absolute
	Item   string `json:"item"`
}

func readBrowserState() BrowserState {
	rd := func(p string) string {
		b, err := os.ReadFile(p)
		if err != nil {
			return ""
		}
		return strings.TrimRight(string(b), "\x00\r\n")
	}
	st := BrowserState{Select: rd(browserSelectFile), Dir: rd(browserDirFile), Item: rd(browserCurrentFile)}
	if st.Dir != "" && !filepath.IsAbs(st.Dir) {
		st.Dir = filepath.Join(browserRoot, st.Dir)
	}
	return st
}

// browserIO is what the browser walk needs; tests replace it.
type browserIO struct {
	press func(key string) error
	typ   func(text string) error
	state func() BrowserState
	wait  func()
}

var liveBrowserIO = browserIO{
	press: PressKey,
	typ:   TypeText,
	state: readBrowserState,
	wait:  func() { time.Sleep(350 * time.Millisecond) },
}

// ClearBrowserState removes stale browser files so that an "active" read
// can only come from the browser opened next.
func ClearBrowserState() {
	for _, p := range []string{browserSelectFile, browserDirFile, browserCurrentFile} {
		os.Remove(p)
	}
}

// filterPrefix is the part of name the browser's type-to-filter can take
// safely: lower-case letters and digits up to the first other character.
// Space is Select in the browser (KEY_SPACE -> select) and '-' / '_' are
// the "previous letter" jump (KEY_MINUS -> minus), so neither may be
// typed; the rest is reached with Down, checking CURRENTPATH each step.
func filterPrefix(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			continue
		}
		break
	}
	return b.String()
}

// highlight puts the browser cursor on the entry called name in the
// current directory, verifying through CURRENTPATH.
func (io browserIO) highlight(name string) error {
	if p := filterPrefix(name); p != "" {
		if err := io.typ(p); err != nil {
			return err
		}
		io.wait()
	} else if err := io.press("home"); err != nil {
		return err
	}
	for i := 0; i < 64; i++ {
		st := io.state()
		if st.Item == name {
			return nil
		}
		if err := io.press("down"); err != nil {
			return err
		}
		io.wait()
		if io.state().Item == st.Item {
			break // end of the list
		}
	}
	return fmt.Errorf("file browser: %q not found in %s", name, io.state().Dir)
}

// BrowseAndSelect drives an open MiSTer file browser to path and selects
// it.  The browser must already be open (FILESELECT=active).  It walks up
// with ".." and down by name, checking FULLPATH/CURRENTPATH after each step,
// and on any mismatch cancels with Esc instead of selecting something else.
func BrowseAndSelect(path string) (BrowserState, error) {
	return liveBrowserIO.browseAndSelect(path)
}

func (io browserIO) browseAndSelect(path string) (st BrowserState, err error) {
	path = filepath.Clean(path)
	dir, name := filepath.Dir(path), filepath.Base(path)
	defer func() {
		if err != nil {
			io.press("esc") // leave nothing half-done in the browser
		}
	}()
	st = io.state()
	if st.Select != "active" {
		return st, fmt.Errorf("file browser not visible to misterclaw (FILESELECT=%q): set log_file_entry=1 in MiSTer.ini (a [core] section is enough) and reload the core", st.Select)
	}
	for step := 0; step < 32 && st.Dir != dir; step++ {
		if strings.HasPrefix(dir, st.Dir+"/") {
			next := strings.SplitN(strings.TrimPrefix(dir, st.Dir+"/"), "/", 2)[0]
			if err = io.highlight(next); err != nil {
				return io.state(), err
			}
		} else {
			if err = io.press("home"); err != nil {
				return st, err
			}
			io.wait()
			if io.state().Item != ".." {
				return io.state(), fmt.Errorf("file browser: cannot go up from %s (first entry is %q, not \"..\")", st.Dir, io.state().Item)
			}
		}
		prev := st.Dir
		if err = io.press("enter"); err != nil {
			return st, err
		}
		io.wait()
		st = io.state()
		if st.Dir == prev {
			return st, fmt.Errorf("file browser: Enter did not change directory (still %s)", prev)
		}
	}
	if st.Dir != dir {
		return st, fmt.Errorf("file browser: could not reach %s (at %s)", dir, st.Dir)
	}
	if err = io.highlight(name); err != nil {
		return io.state(), err
	}
	if err = io.press("enter"); err != nil {
		return st, err
	}
	io.wait()
	st = io.state()
	if st.Select != "selected" {
		return st, fmt.Errorf("file browser: selecting %s did not report \"selected\" (FILESELECT=%q)", name, st.Select)
	}
	return st, nil
}
