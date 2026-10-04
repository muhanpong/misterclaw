package mister

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// OSDMask is a core's 16-bit OSD mask (UIO_GET_OSDMASK) as far as it is
// known: bits outside Known are unknown.  MiSTer main reads the mask from
// the core and does not publish it, so a caller either supplies it or a
// core-specific rule infers part of it.
type OSDMask struct {
	Value uint32 `json:"value"`
	Known uint32 `json:"known"`
}

// FullMask is a mask the caller knows completely.
func FullMask(v uint32) OSDMask { return OSDMask{Value: v, Known: 0xFFFFFFFF} }

func (m *OSDMask) set(bit int, v bool) {
	m.Known |= 1 << uint(bit)
	if v {
		m.Value |= 1 << uint(bit)
	} else {
		m.Value &^= 1 << uint(bit)
	}
}

// ---- MSX1 core (muhanpong/MSX1_MiSTer) ---------------------------------------
//
// Mirrors status_menumask in MSX1.sv and the slot logic in rtl/msx_config.sv
// (2ce73fc).  Inputs: the core's status bits as saved in MSX1.CFG, and the
// machine pack MiSTer loads at start (config/MSX1.f1).  Bit 6 needs the SRAM
// sizes the core works out during upload and bit 15 whether an MT32-pi
// answers on the USER port; both stay unknown.

// MSX1PackRecordPath is where MiSTer main remembers the FC1 ("Load ROM
// PACK") file, which it loads again when the core starts (var for tests).
var MSX1PackRecordPath = "/media/fat/config/MSX1.f1"

// MSX1PackInfo is what the core takes from a machine pack for the mask.
type MSX1PackInfo struct {
	Path   string `json:"path"`
	Ver    int    `json:"ver"`     // BIOS byte 002Dh: 0 MSX1, 1 MSX2, 2 MSX2+, 3 turbo R
	UseFDC bool   `json:"use_fdc"` // a block with the FDC memory device
	MIDI   bool   `json:"midi"`    // the FS-A1GT built-in MSX-MIDI (device id 5)
}

// ParseMSX1Pack walks a .MSX machine pack the way memory_upload.sv does:
// 16-byte "MSX" headers, ROM/FDC/device payloads of size*16 KB in between.
func ParseMSX1Pack(path string) (*MSX1PackInfo, error) {
	d, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	info := &MSX1PackInfo{Path: path, Ver: -1}
	p := 0
	for p+16 <= len(d) {
		h := d[p : p+16]
		if string(h[:3]) != "MSX" {
			return nil, fmt.Errorf("%s: no record header at offset %d", path, p)
		}
		typ, ss := h[3]>>4, h[3]&15
		size := (int(h[5]&7)<<8 | int(h[6])) * 16384
		p += 16
		hasPayload := func() bool {
			// Mirror headers carry a size but no payload; a payload never
			// starts with another record header.
			return size > 0 && p+size <= len(d) && !(p+3 <= len(d) && string(d[p:p+3]) == "MSX")
		}
		switch typ {
		case 7: // DEVICE
			if h[7] == 5 {
				info.MIDI = true
			}
			if hasPayload() {
				p += size
			}
		case 4: // SLOT_INTERNAL
			if h[7] == 5 {
				info.MIDI = true
			}
			if h[4] == 3 { // DEVICE_FDC
				info.UseFDC = true
			}
			if (h[4] == 1 || h[4] == 3) && hasPayload() {
				// rec_bios: slot 0-0 with page 0 mapped, first such record
				if ss == 0 && h[9]&3 != 0 && info.Ver < 0 && size > 0x2D {
					info.Ver = int(d[p+0x2D])
				}
				p += size
			}
		case 5: // KBD layout: payload up to the next header
			q := strings.Index(string(d[p:]), "MSX")
			if q < 0 {
				p = len(d)
			} else {
				p += q
			}
		}
	}
	if info.Ver < 0 {
		return nil, fmt.Errorf("%s: no slot 0-0 BIOS record", path)
	}
	return info, nil
}

// LoadMSX1MachinePack parses the pack named in MSX1PackRecordPath.
func LoadMSX1MachinePack() (*MSX1PackInfo, error) {
	b, err := os.ReadFile(MSX1PackRecordPath)
	if err != nil {
		return nil, err
	}
	rel := strings.TrimSpace(strings.TrimRight(string(b), "\x00"))
	if i := strings.IndexByte(rel, 0); i >= 0 {
		rel = rel[:i]
	}
	if rel == "" {
		return nil, fmt.Errorf("%s is empty", MSX1PackRecordPath)
	}
	if !filepath.IsAbs(rel) {
		rel = filepath.Join("/media/fat", rel)
	}
	return ParseMSX1Pack(rel)
}

// InferMSX1Mask works out the MSX1 core's OSD mask from its status bits
// (cfg) and, if known, its machine pack.  notes say what was assumed.
func InferMSX1Mask(cfg []byte, pack *MSX1PackInfo) (OSDMask, []string) {
	var m OSDMask
	notes := []string{"status bits taken from the saved .CFG; OSD changes not yet saved with 'Save settings' are not seen"}
	st := func(lo, hi int) int { return GetBitRange(cfg, lo, hi) }

	expA, expB := st(71, 71) == 1, st(72, 72) == 1
	slotA, slotB := st(17, 19), st(29, 31)
	mapperA := st(20, 23)

	// typ_A: values >= 6 (FDC / Empty) depend on the pack's FDC.
	const typROM, typFDC, typEmpty = 0, 6, 7
	typA, typAKnown := slotA, true
	if slotA >= typFDC {
		if pack == nil {
			typAKnown = false
		} else if pack.UseFDC || slotA != typFDC {
			typA = typEmpty
		} else {
			typA = typFDC
		}
	}
	typB := typEmpty
	if slotB < 4 {
		typB = slotB
	} else if slotB == 5 {
		typB = 8 // MU-PACK
	}

	// Sub-slot walk (msx_config.sv): first ROM/SCC takes the file, ROM sets
	// rom*_used; later ROM/SCC entries become None.
	walk := func(base int, maxDev int, exp bool) (fileUsed, romUsed bool) {
		if !exp {
			return
		}
		for i := 0; i < 4; i++ {
			d := st(base+3*i, base+3*i+2)
			if d > maxDev {
				d = 0
			}
			if d == 1 || d == 2 { // ROM, SCC
				if fileUsed {
					continue
				}
				fileUsed = true
				if d == 1 {
					romUsed = true
				}
			}
		}
		return
	}
	fileAUsed, romAUsed := walk(73, 5, expA)
	fileBUsed, romBUsed := walk(85, 4, expB)

	m.set(0, st(8, 8) == 1)
	m.set(7, expA)
	m.set(8, expB)
	m.set(14, false)

	if expA || typAKnown {
		fileA := fileAUsed
		romA := romAUsed
		if !expA {
			fileA, romA = typA == typROM, typA == typROM
		}
		m.set(3, !fileA)
		m.set(11, !romA)
		switch {
		case !romA || mapperA == 0 || mapperA == 10 || mapperA == 11 || mapperA == 12:
			m.set(5, true)
		case mapperA == 3:
			notes = append(notes, "bit 5 unknown: ASCII16X hides SRAM size only for a ROM over 4 MB")
		default:
			m.set(5, false)
		}
	}
	fileB, romB := fileBUsed, romBUsed
	if !expB {
		fileB, romB = typB == typROM, typB == typROM
	}
	m.set(4, !fileB)
	m.set(12, !romB)

	muPackB := slotB == 5 && !expB
	if pack != nil {
		m.set(1, pack.UseFDC || (!expA && typA == typFDC))
		m.set(2, pack.UseFDC)
		m.set(10, pack.MIDI)
		m.set(9, pack.MIDI || muPackB)
		m.set(13, pack.Ver == 3)
	} else {
		notes = append(notes, "machine pack unknown: bits 1, 2, 9, 10, 13 not inferred")
		if muPackB {
			m.set(9, true)
		}
	}
	notes = append(notes, "bit 6 (SRAM sizes) and bit 15 (MT32-pi answering) are not inferred")
	return m, notes
}
