package server

import (
	"bufio"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/catallo/misterclaw/pkg/mister"
	"github.com/catallo/misterclaw/pkg/session"
	"github.com/google/uuid"
)

// Request represents an incoming JSON command.
type Request struct {
	// Command execution
	ID      string `json:"id,omitempty"`
	Cmd     string `json:"cmd,omitempty"`
	Session string `json:"session,omitempty"`
	Pty     *bool  `json:"pty,omitempty"`
	Agent   string `json:"agent,omitempty"`

	// Input forwarding
	Input string `json:"input,omitempty"`

	// Session management
	List  *bool `json:"list,omitempty"`
	Kill  *bool `json:"kill,omitempty"`
	Close *bool `json:"close,omitempty"`
	Drain *bool `json:"drain,omitempty"` // cancel queued cmds + kill running (session-kill)

	// File transfer (push = host->MiSTer, pull = MiSTer->host). Chunked
	// base64 over this line protocol; SHA256 verified, atomic-renamed.
	Push     *bool  `json:"push,omitempty"`      // begin push (with Path, Size, Sha256)
	PushData string `json:"push_data,omitempty"` // one base64 chunk
	PushDone *bool  `json:"push_done,omitempty"` // finish push: verify + rename
	Pull     *bool  `json:"pull,omitempty"`      // pull Path to host (server streams chunks)
	Size     int64  `json:"size,omitempty"`      // expected byte count (push)
	Sha256   string `json:"sha256,omitempty"`    // expected hex digest (push)

	// PTY resize
	Resize *ResizeRequest `json:"resize,omitempty"`

	// MiSTer commands
	MiSTer   string   `json:"mister,omitempty"`
	Core     string   `json:"core,omitempty"`
	Path     string   `json:"path,omitempty"`
	Query    string   `json:"query,omitempty"`
	System   string   `json:"system,omitempty"`
	Action   string   `json:"action,omitempty"`
	URL      string   `json:"url,omitempty"`
	Hostname string   `json:"hostname,omitempty"`
	Key      string   `json:"key,omitempty"`
	Raw      *int     `json:"raw,omitempty"`
	Combo    []string `json:"combo,omitempty"`
	Device   string   `json:"device,omitempty"`
	Button   string   `json:"button,omitempty"`
	DPad     string   `json:"dpad,omitempty"`
	Text     string   `json:"text,omitempty"`
	Layout   string   `json:"layout,omitempty"` // text: "jis" for a JIS-layout machine

	// OSD navigation
	Target string `json:"target,omitempty"`
	// The core's OSD mask (the H/h row conditions), when the caller knows
	// it.  MiSTer main does not publish it; without it, positions that
	// depend on conditional rows are refused.
	OSDMask *uint32 `json:"osd_mask,omitempty"`
	// msx1_overlay: turn Debug Overlay on for the reading and off again
	Toggle bool `json:"toggle,omitempty"`

	// CFG commands
	Option   string `json:"option,omitempty"`
	Value    string `json:"value,omitempty"`
	Location string `json:"location,omitempty"`
}

// ResizeRequest holds PTY dimensions.
type ResizeRequest struct {
	Cols int `json:"cols"`
	Rows int `json:"rows"`
}

// Server is a TCP server handling the MisterClaw JSON protocol.
type Server struct {
	listener net.Listener
	manager  *session.Manager
	clients  map[net.Conn]struct{}
	mu       sync.Mutex
}

// New creates a new Server.
func New(manager *session.Manager) *Server {
	return &Server{
		manager: manager,
		clients: make(map[net.Conn]struct{}),
	}
}

// ListenAndServe starts the TCP server.
func (s *Server) ListenAndServe(addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	s.listener = ln
	log.Printf("listening on %s", addr)

	for {
		conn, err := ln.Accept()
		if err != nil {
			// Listener was closed during shutdown
			select {
			default:
				return err
			}
		}
		s.mu.Lock()
		s.clients[conn] = struct{}{}
		s.mu.Unlock()

		log.Printf("client connected: %s", conn.RemoteAddr())
		go s.handleConn(conn)
	}
}

// Close shuts down the server and all client connections.
func (s *Server) Close() {
	if s.listener != nil {
		s.listener.Close()
	}
	s.mu.Lock()
	for conn := range s.clients {
		conn.Close()
	}
	s.mu.Unlock()
}

func (s *Server) handleConn(conn net.Conn) {
	// Track which sessions THIS connection fed commands into, so they can be
	// drained if the client vanishes (task #18 abandoned-queue hazard: a
	// killed/TaskStop'd client — e.g. the golden suite — used to leave its
	// queued commands running, blanket-killing the user's live hack).
	usedSessions := make(map[string]struct{})
	var usedMu sync.Mutex

	// In-progress host->device push for this connection (task #36). Cleaned
	// up on disconnect so an interrupted transfer leaves no stray temp file.
	var push *pushState

	defer func() {
		conn.Close()
		push.cleanup()
		s.mu.Lock()
		delete(s.clients, conn)
		s.mu.Unlock()

		usedMu.Lock()
		names := make([]string, 0, len(usedSessions))
		for name := range usedSessions {
			names = append(names, name)
		}
		usedMu.Unlock()
		for _, name := range names {
			if n := s.manager.Drain(name); n > 0 {
				log.Printf("client %s gone: drained %d queued command(s) from session %q", conn.RemoteAddr(), n, name)
			}
		}

		log.Printf("client disconnected: %s", conn.RemoteAddr())
	}()

	// connMu serializes writes to this connection
	var connMu sync.Mutex
	send := func(v interface{}) {
		data, err := json.Marshal(v)
		if err != nil {
			return
		}
		connMu.Lock()
		conn.Write(append(data, '\n'))
		connMu.Unlock()
	}

	scanner := bufio.NewScanner(conn)
	// Allow large lines (1MB) for base64 data etc.
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var req Request
		if err := json.Unmarshal(line, &req); err != nil {
			send(map[string]interface{}{
				"error": fmt.Sprintf("invalid JSON: %v", err),
			})
			continue
		}

		// File-transfer messages carry per-connection push state, so they
		// are handled here rather than in the stateless dispatch (task #36).
		switch {
		case req.Push != nil && *req.Push:
			push.cleanup() // abandon any prior incomplete push on this conn
			push = beginPush(req, send)
			continue
		case req.PushData != "":
			push.writeChunk(req.PushData)
			continue
		case req.PushDone != nil && *req.PushDone:
			push.finish(send)
			push = nil
			continue
		case req.Pull != nil && *req.Pull:
			s.handlePull(req, send)
			continue
		}

		// Remember sessions this connection ran commands in.
		if req.Cmd != "" {
			name := req.Session
			if name == "" {
				name = "default"
			}
			usedMu.Lock()
			usedSessions[name] = struct{}{}
			usedMu.Unlock()
		}

		s.dispatch(req, send)
	}
}

func (s *Server) dispatch(req Request, send func(interface{})) {
	switch {
	case req.List != nil && *req.List:
		s.handleList(send)

	case req.Kill != nil && *req.Kill:
		s.handleKill(req, send)

	case req.Close != nil && *req.Close:
		s.handleClose(req, send)

	case req.Drain != nil && *req.Drain:
		s.handleDrain(req, send)

	case req.Resize != nil:
		s.handleResize(req, send)

	case req.Input != "":
		s.handleInput(req, send)

	case req.MiSTer != "":
		s.handleMiSTer(req, send)

	case req.Cmd != "":
		s.handleCmd(req, send)

	default:
		send(map[string]interface{}{
			"error": "unrecognized command",
		})
	}
}

func (s *Server) handleList(send func(interface{})) {
	sessions := s.manager.List()
	send(map[string]interface{}{
		"list":     true,
		"sessions": sessions,
		"total":    len(sessions),
	})
}

func (s *Server) handleKill(req Request, send func(interface{})) {
	success := s.manager.Kill(req.Session)
	send(map[string]interface{}{
		"kill":    true,
		"session": req.Session,
		"success": success,
	})
}

func (s *Server) handleClose(req Request, send func(interface{})) {
	success := s.manager.Close(req.Session)
	send(map[string]interface{}{
		"close":   true,
		"session": req.Session,
		"success": success,
	})
}

// handleDrain cancels a session's queued commands and kills the running one
// (session-kill). Unlike Kill (running process only), this flushes the whole
// backlog — the manual recovery for a runaway queue (task #18).
func (s *Server) handleDrain(req Request, send func(interface{})) {
	if req.Session == "" {
		send(map[string]interface{}{"error": "drain requires session"})
		return
	}
	cancelled := s.manager.Drain(req.Session)
	if cancelled < 0 {
		send(map[string]interface{}{
			"drain":   true,
			"session": req.Session,
			"success": false,
			"error":   fmt.Sprintf("session %q not found", req.Session),
		})
		return
	}
	send(map[string]interface{}{
		"drain":     true,
		"session":   req.Session,
		"success":   true,
		"cancelled": cancelled,
	})
}

func (s *Server) handleResize(req Request, send func(interface{})) {
	if req.Session == "" {
		send(map[string]interface{}{"error": "resize requires session"})
		return
	}
	err := s.manager.Resize(req.Session, uint16(req.Resize.Cols), uint16(req.Resize.Rows))
	if err != nil {
		send(map[string]interface{}{"error": err.Error()})
		return
	}
	send(map[string]interface{}{"resized": true, "session": req.Session})
}

func (s *Server) handleInput(req Request, send func(interface{})) {
	sessionName := req.Session
	if sessionName == "" && req.ID != "" {
		// Input can target by ID — but we route by session name
		send(map[string]interface{}{"error": "input requires session"})
		return
	}
	err := s.manager.WriteInput(sessionName, []byte(req.Input))
	if err != nil {
		send(map[string]interface{}{"error": err.Error()})
	}
}

func (s *Server) handleCmd(req Request, send func(interface{})) {
	sessionName := req.Session
	if sessionName == "" {
		sessionName = "default"
	}

	id := req.ID
	if id == "" {
		id = uuid.New().String()
	}

	usePty := true
	if req.Pty != nil {
		usePty = *req.Pty
	}

	outputCb := func(data []byte) {
		send(map[string]interface{}{
			"id":     id,
			"stream": "stdout",
			"data":   string(data),
		})
	}

	doneCb := func(exitCode int) {
		send(map[string]interface{}{
			"id":        id,
			"done":      true,
			"exit_code": exitCode,
			"sessions":  s.manager.List(),
		})
	}

	s.manager.Execute(sessionName, req.Cmd, usePty, req.Agent, outputCb, doneCb)
}

func (s *Server) handleMiSTer(req Request, send func(interface{})) {
	switch req.MiSTer {
	case "load_core":
		path := req.Path
		if path == "" && req.Core != "" {
			path = req.Core
		}
		if path == "" {
			send(map[string]interface{}{"error": "load_core requires path or core"})
			return
		}
		status, err := mister.LoadCoreVerified(path, 5*time.Second)
		if err != nil {
			send(map[string]interface{}{
				"mister":  "load_core",
				"success": false,
				"error":   err.Error(),
			})
			return
		}
		send(map[string]interface{}{
			"mister":    "load_core",
			"success":   true,
			"core_name": status.CoreName,
			"core_path": status.CorePath,
			"game_path": status.GamePath,
		})

	case "status":
		status, err := mister.GetRunningCore()
		if err != nil {
			send(map[string]interface{}{"error": err.Error()})
			return
		}
		send(map[string]interface{}{
			"mister":    "status",
			"core_name": status.CoreName,
			"core_path": status.CorePath,
			"game_path": status.GamePath,
		})

	case "screenshot":
		result, err := mister.TakeScreenshotAndCapture(5 * time.Second)
		if err != nil {
			send(map[string]interface{}{"error": err.Error()})
			return
		}
		send(map[string]interface{}{
			"mister":   "screenshot",
			"success":  true,
			"data":     result.Data,
			"core":     result.CoreName,
			"filename": result.FileName,
			"size":     result.SizeBytes,
		})

	case "info":
		info := mister.GetSystemInfo()
		send(info)

	case "systems":
		stats := mister.GetSystemStats()
		if stats == nil && !mister.IsDiscoveryReady() {
			send(map[string]interface{}{
				"mister":  "systems",
				"status":  "pending",
				"message": "System discovery is in progress. This may take several minutes on large ROM collections. Try again shortly.",
			})
			return
		}
		send(map[string]interface{}{
			"mister":   "systems",
			"systems":  stats,
			"complete": mister.IsDiscoveryComplete(),
		})

	case "search":
		if !mister.IsGamesReady() {
			send(map[string]interface{}{
				"mister":  "search",
				"status":  "pending",
				"message": "Game library scan is in progress. This may take several minutes on large ROM collections. Results will be available once the scan completes.",
			})
			return
		}
		results := mister.SearchGames(req.Query, req.System)
		send(map[string]interface{}{
			"mister":  "search",
			"results": results,
			"total":   len(results),
		})

	case "launch":
		var game *mister.GameInfo
		if req.Path != "" && req.System != "" {
			base := filepath.Base(req.Path)
			name := strings.TrimSuffix(base, filepath.Ext(base))
			game = &mister.GameInfo{
				Name:   name,
				Path:   req.Path,
				System: req.System,
			}
		} else if req.Query != "" {
			// Launch by search (first match)
			results := mister.SearchGames(req.Query, req.System)
			if len(results) > 0 {
				game = &results[0]
			}
		}

		if game == nil {
			if !mister.IsGamesReady() {
				send(map[string]interface{}{
					"mister":  "launch",
					"success": false,
					"status":  "pending",
					"message": "Game library scan is in progress. The game may exist but hasn't been indexed yet. This may take several minutes on large ROM collections.",
				})
				return
			}
			send(map[string]interface{}{
				"mister":  "launch",
				"success": false,
				"error":   "no game found or missing parameters",
			})
			return
		}

		cfg, _ := mister.GetSystemConfig(game.System)
		err := mister.LaunchGame(*game)
		if err != nil {
			send(map[string]interface{}{
				"mister":  "launch",
				"success": false,
				"error":   err.Error(),
			})
			return
		}
		send(map[string]interface{}{
			"mister":    "launch",
			"success":   true,
			"game":      game.Name,
			"core_name": cfg.Core,
		})

	case "input":
		switch {
		case req.Button != "":
			// "button" field is shorthand for gamepad
			if err := mister.PressGamepadButton(req.Button); err != nil {
				send(map[string]interface{}{
					"mister":  "input",
					"success": false,
					"error":   err.Error(),
				})
				return
			}
			send(map[string]interface{}{
				"mister":  "input",
				"success": true,
				"button":  req.Button,
				"device":  "gamepad",
			})
		case req.DPad != "":
			// "dpad" field uses gamepad (explicit or default)
			if err := mister.GamepadDPad(req.DPad); err != nil {
				send(map[string]interface{}{
					"mister":  "input",
					"success": false,
					"error":   err.Error(),
				})
				return
			}
			send(map[string]interface{}{
				"mister":  "input",
				"success": true,
				"dpad":    req.DPad,
				"device":  "gamepad",
			})
		case req.Key != "":
			if req.Device == "gamepad" {
				if err := mister.PressGamepadButton(req.Key); err != nil {
					send(map[string]interface{}{
						"mister":  "input",
						"success": false,
						"error":   err.Error(),
					})
					return
				}
				send(map[string]interface{}{
					"mister":  "input",
					"success": true,
					"key":     req.Key,
					"device":  "gamepad",
				})
			} else {
				if err := mister.PressKey(req.Key); err != nil {
					send(map[string]interface{}{
						"mister":  "input",
						"success": false,
						"error":   err.Error(),
					})
					return
				}
				send(map[string]interface{}{
					"mister":  "input",
					"success": true,
					"key":     req.Key,
				})
			}
		case req.Raw != nil:
			if err := mister.PressRawKey(*req.Raw); err != nil {
				send(map[string]interface{}{
					"mister":  "input",
					"success": false,
					"error":   err.Error(),
				})
				return
			}
			send(map[string]interface{}{
				"mister":  "input",
				"success": true,
				"raw":     *req.Raw,
			})
		case len(req.Combo) > 0:
			if err := mister.PressCombo(req.Combo); err != nil {
				send(map[string]interface{}{
					"mister":  "input",
					"success": false,
					"error":   err.Error(),
				})
				return
			}
			send(map[string]interface{}{
				"mister":  "input",
				"success": true,
				"combo":   req.Combo,
			})
		case req.Text != "":
			text := req.Text
			if req.Layout == "jis" {
				t, err := mister.TranslateForJIS(text)
				if err != nil {
					send(map[string]interface{}{"mister": "input", "success": false, "error": err.Error()})
					return
				}
				text = t
			}
			if err := mister.TypeText(text); err != nil {
				send(map[string]interface{}{
					"mister":  "input",
					"success": false,
					"error":   err.Error(),
				})
				return
			}
			send(map[string]interface{}{
				"mister":  "input",
				"success": true,
				"text":    req.Text,
			})
		default:
			send(map[string]interface{}{
				"error": "input requires key, raw, combo, button, dpad, or text parameter",
			})
		}

	case "tailscale":
		switch req.Action {
		case "setup":
			authURL, err := mister.TailscaleSetup(req.URL, req.Hostname)
			if err != nil {
				send(map[string]interface{}{
					"mister":  "tailscale",
					"action":  "setup",
					"success": false,
					"error":   err.Error(),
				})
				return
			}
			resp := map[string]interface{}{
				"mister":  "tailscale",
				"action":  "setup",
				"success": true,
			}
			if authURL != "" {
				resp["auth_url"] = authURL
			} else {
				// Already authenticated — include IP
				status, err := mister.TailscaleGetStatus()
				if err == nil && status.IP != "" {
					resp["ip"] = status.IP
				}
			}
			send(resp)

		case "status":
			status, err := mister.TailscaleGetStatus()
			if err != nil {
				send(map[string]interface{}{"error": err.Error()})
				return
			}
			send(status)

		case "start":
			err := mister.TailscaleStart()
			if err != nil {
				send(map[string]interface{}{
					"mister":  "tailscale",
					"action":  "start",
					"success": false,
					"error":   err.Error(),
				})
				return
			}
			send(map[string]interface{}{
				"mister":  "tailscale",
				"action":  "start",
				"success": true,
			})

		case "stop":
			err := mister.TailscaleStop()
			if err != nil {
				send(map[string]interface{}{
					"mister":  "tailscale",
					"action":  "stop",
					"success": false,
					"error":   err.Error(),
				})
				return
			}
			send(map[string]interface{}{
				"mister":  "tailscale",
				"action":  "stop",
				"success": true,
			})

		default:
			send(map[string]interface{}{
				"error": fmt.Sprintf("unknown tailscale action: %s", req.Action),
			})
		}

	case "osd_info":
		var running *mister.CoreStatus
		coreName := req.Core
		if coreName == "" {
			// Use currently running core
			status, err := mister.GetRunningCore()
			if err != nil {
				send(map[string]interface{}{"error": "no core specified and " + err.Error()})
				return
			}
			running = status
			coreName = status.LookupName()
		}

		db, err := mister.GetConfStrDB()
		if err != nil {
			send(map[string]interface{}{
				"mister":  "osd_info",
				"success": false,
				"error":   fmt.Sprintf("confstr database not available: %v", err),
			})
			return
		}

		var res *mister.ResolvedOSD
		if running != nil {
			res, err = mister.ResolveRunningOSD(db, running, false)
		} else {
			res, err = mister.ResolveNamedOSD(db, coreName, false)
		}
		if err != nil {
			send(map[string]interface{}{
				"mister":  "osd_info",
				"success": false,
				"error":   err.Error(),
			})
			return
		}
		osd := res.OSD

		send(map[string]interface{}{
			"mister":       "osd_info",
			"success":      true,
			"osd_source":   res.Source,
			"core_name":    osd.CoreName,
			"repo":         osd.Repo,
			"conf_str_raw": osd.ConfStrRaw,
			"menu":         osd.Menu,
		})

	case "osd_visible":
		s.handleOSDVisible(req, send)

	case "cfg_read":
		s.handleCFGRead(req, send)

	case "cfg_write":
		s.handleCFGWrite(req, send)

	case "reload":
		s.handleReload(req, send)

	case "rescan":
		s.handleRescan(req, send)

	case "osd_navigate":
		target := req.Target
		if target == "" {
			send(map[string]interface{}{"error": "osd_navigate requires target"})
			return
		}
		// Resolve strictly: a wrong layout means key presses on the wrong rows.
		ctx, ok := s.resolveCore(req, send, true)
		if !ok {
			return
		}
		coreName := ctx.OSD.CoreName
		mask, maskInfo := osdMaskFor(req, ctx)
		if err := mister.OSDNavigateToOSD(ctx.OSD, mask, target); err != nil {
			send(map[string]interface{}{
				"mister":   "osd_navigate",
				"success":  false,
				"error":    err.Error(),
				"osd_mask": maskInfo,
			})
			return
		}
		send(map[string]interface{}{
			"mister":   "osd_navigate",
			"success":  true,
			"target":   target,
			"core":     coreName,
			"osd_mask": maskInfo,
		})

	case "mount":
		s.handleMount(req, send)

	case "msx1_overlay":
		s.handleMSX1Overlay(req, send)

	case "system_info":
		system := req.System
		if system == "" {
			send(map[string]interface{}{"error": "system_info requires system"})
			return
		}
		cfg, ok := mister.GetSystemConfig(system)
		if !ok {
			send(map[string]interface{}{
				"mister":  "system_info",
				"success": false,
				"error":   fmt.Sprintf("unknown system: %s", system),
			})
			return
		}
		resp := map[string]interface{}{
			"mister":  "system_info",
			"success": true,
			"system":  system,
			"config":  cfg,
		}
		if cfg.PostLaunch != nil && cfg.PostLaunch.Notes != "" {
			resp["notes"] = cfg.PostLaunch.Notes
		}
		db, err := mister.GetConfStrDB()
		if err == nil {
			coreName := filepath.Base(cfg.Core)
			osd := mister.LookupCoreOSD(db, coreName)
			if osd != nil {
				resp["core_name"] = osd.CoreName
				resp["menu"] = osd.Menu
			}
		}
		send(resp)

	default:
		send(map[string]interface{}{
			"error": fmt.Sprintf("unknown mister command: %s", req.MiSTer),
		})
	}
}

// coreContext holds all resolved state for the current core: OSD info, CFG data, DIP data, paths.

// reloadCurrentCore reloads the currently running core/game so config changes take effect.
func (s *Server) handleReload(req Request, send func(interface{})) {
	status, err := mister.GetRunningCore()
	if err != nil || status == nil {
		send(map[string]interface{}{"mister": "reload", "success": false, "error": "no core running"})
		return
	}
	path := status.GamePath
	if path == "" {
		path = status.CorePath
	}
	if path == "" {
		send(map[string]interface{}{"mister": "reload", "success": false, "error": "no core path found"})
		return
	}
	mister.LoadCore(path)
	send(map[string]interface{}{"mister": "reload", "success": true, "path": path})
}

func (s *Server) handleRescan(req Request, send func(interface{})) {
	location := req.Location
	if location == "" {
		mister.InvalidateCache()
		// Respond immediately — scan runs in background
		send(map[string]interface{}{
			"mister":   "rescan",
			"success":  true,
			"message":  "Rescan started in background. Discovery may take several minutes on large ROM collections. Use 'systems' to check progress.",
			"location": "all",
		})
		return
	}

	systemsFound := mister.RescanLocation(location)
	send(map[string]interface{}{
		"mister":        "rescan",
		"success":       true,
		"systems_found": systemsFound,
		"location":      location,
	})
}

type coreContext struct {
	OSD       *mister.CoreOSD
	OSDSource string // "sidecar", "database" or "database-fuzzy"
	CFGData   []byte
	CFGPath   string
	MRAPath   string // empty if not arcade
	MRA       *mister.MRA
	DIPData   []byte // nil if no MRA/DIP switches
	DIPPath   string // empty if no MRA
}

// resolveCore resolves the current core's OSD info, CFG file, and DIP file.
// strict (writes, OSD navigation) accepts exact name matches or a per-build
// sidecar only; read-only callers may fall back to similar names.
func (s *Server) resolveCore(req Request, send func(interface{}), strict bool) (*coreContext, bool) {
	coreName := req.Core
	cfgName := ""
	mraPath := ""
	var mra *mister.MRA
	var running *mister.CoreStatus
	if coreName == "" {
		status, err := mister.GetRunningCore()
		if err != nil {
			send(map[string]interface{}{"error": "no core specified and " + err.Error()})
			return nil, false
		}
		running = status
		coreName = status.LookupName()
		// CFG name: what MiSTer main itself uses (/tmp/CORENAME) when known.
		// Otherwise it comes from the game (MRA), not the core.
		if status.ConfigName != "" {
			cfgName = status.ConfigName
			if strings.HasSuffix(strings.ToLower(status.GamePath), ".mra") {
				mraPath = status.GamePath
				if parsed, err := mister.ParseMRA(status.GamePath); err == nil {
					mra = parsed
				}
			}
		} else if status.GamePath != "" {
			if strings.HasSuffix(strings.ToLower(status.GamePath), ".mra") {
				mraPath = status.GamePath
				if parsed, err := mister.ParseMRA(status.GamePath); err == nil {
					mra = parsed
					if mra.SetName != "" {
						cfgName = mra.SetName
					}
				}
			}
			// Fallback: use MRA filename without extension
			if cfgName == "" {
				base := filepath.Base(status.GamePath)
				cfgName = strings.TrimSuffix(base, filepath.Ext(base))
			}
		} else {
			cfgName = coreName
		}
	} else {
		cfgName = coreName
	}

	db, err := mister.GetConfStrDB()
	if err != nil {
		send(map[string]interface{}{"error": fmt.Sprintf("confstr database not available: %v", err)})
		return nil, false
	}

	var res *mister.ResolvedOSD
	if running != nil {
		res, err = mister.ResolveRunningOSD(db, running, strict)
	} else {
		res, err = mister.ResolveNamedOSD(db, coreName, strict)
	}
	if err != nil {
		send(map[string]interface{}{"error": err.Error()})
		return nil, false
	}
	osd := res.OSD

	cfgPath := mister.CFGPath(cfgName)
	cfgData, err := mister.ReadCFG(cfgPath)
	if err != nil {
		// If no CFG exists, use all zeros (default state)
		cfgData = make([]byte, 16)
	}

	ctx := &coreContext{
		OSD:       osd,
		OSDSource: res.Source,
		CFGData:   cfgData,
		CFGPath:   cfgPath,
		MRAPath:   mraPath,
		MRA:       mra,
	}

	// Load DIP data if this is an arcade game with an MRA
	if mra != nil && mraPath != "" {
		dipPath := mister.DIPPath(mraPath)
		ctx.DIPPath = dipPath
		ctx.DIPData = mister.LoadDIPData(dipPath, mra)
	}

	return ctx, true
}

func (s *Server) handleOSDVisible(req Request, send func(interface{})) {
	ctx, ok := s.resolveCore(req, send, false)
	if !ok {
		return
	}

	// Row visibility (H/h) follows the core's OSD mask, not .CFG bits.
	// Rows that depend on bits nobody knows are listed separately.
	mask, maskInfo := osdMaskFor(req, ctx)
	var visible, dependent []mister.MenuItem
	for _, it := range ctx.OSD.Menu {
		if !mister.IsListedMenuItem(it) {
			continue
		}
		switch mister.RowState(it, mask) {
		case 1:
			visible = append(visible, it)
		case -1:
			dependent = append(dependent, it)
		}
	}
	resp := map[string]interface{}{
		"mister":     "osd_visible",
		"success":    true,
		"core_name":  ctx.OSD.CoreName,
		"osd_source": ctx.OSDSource,
		"menu":       visible,
		"osd_mask":   maskInfo,
	}
	if len(dependent) > 0 {
		resp["mask_dependent"] = dependent
		resp["note"] = "rows in mask_dependent are shown or hidden by the core's OSD mask; pass osd_mask to resolve them"
	}
	send(resp)
}

func (s *Server) handleCFGRead(req Request, send func(interface{})) {
	ctx, ok := s.resolveCore(req, send, false)
	if !ok {
		return
	}

	// Decode core option values from CFG bits
	settings := []map[string]interface{}{}
	for _, item := range ctx.OSD.Menu {
		if item.Type != "option" && item.Type != "option_hidden" {
			continue
		}
		val := mister.GetBitRange(ctx.CFGData, item.Bit, item.BitHigh)
		opt := map[string]interface{}{
			"name":   item.Name,
			"bit":    item.Bit,
			"value":  val,
			"source": "cfg",
		}
		if val < len(item.Values) {
			opt["value_name"] = item.Values[val]
		}
		if len(item.Values) > 0 {
			opt["values"] = item.Values
		}
		settings = append(settings, opt)
	}

	// Include DIP switches from MRA — read from separate .dip data
	if ctx.MRA != nil {
		dips := mister.ParseDIPSwitches(ctx.MRA)
		for _, dip := range dips {
			val := mister.GetBitRange(ctx.DIPData, dip.Bit, dip.BitHigh)
			opt := map[string]interface{}{
				"name":   dip.Name,
				"bit":    dip.Bit,
				"value":  val,
				"source": "dip",
			}
			if val < len(dip.Values) {
				opt["value_name"] = dip.Values[val]
			}
			if len(dip.Values) > 0 {
				opt["values"] = dip.Values
			}
			settings = append(settings, opt)
		}
	}

	resp := map[string]interface{}{
		"mister":     "cfg_read",
		"success":    true,
		"core_name":  ctx.OSD.CoreName,
		"osd_source": ctx.OSDSource,
		"cfg_path":   ctx.CFGPath,
		"cfg_hex":    hex.EncodeToString(ctx.CFGData),
		"cfg_size":   len(ctx.CFGData),
		"settings":   settings,
	}
	if ctx.DIPPath != "" {
		resp["dip_path"] = ctx.DIPPath
		resp["dip_hex"] = hex.EncodeToString(ctx.DIPData)
	}
	send(resp)
}

func (s *Server) handleCFGWrite(req Request, send func(interface{})) {
	if req.Option == "" {
		send(map[string]interface{}{"error": "cfg_write requires option parameter"})
		return
	}
	if req.Value == "" {
		send(map[string]interface{}{"error": "cfg_write requires value parameter"})
		return
	}

	ctx, ok := s.resolveCore(req, send, true)
	if !ok {
		return
	}

	// Try CONF_STR options first → write to .CFG file
	item := mister.FindOption(ctx.OSD, req.Option)
	if item != nil {
		valIdx := mister.FindOptionValue(item, req.Value)
		if valIdx < 0 {
			send(map[string]interface{}{
				"mister":  "cfg_write",
				"success": false,
				"error":   fmt.Sprintf("value %q not found for option %s (available: %v)", req.Value, req.Option, item.Values),
			})
			return
		}

		mister.SetBitRange(ctx.CFGData, item.Bit, item.BitHigh, valIdx)

		if err := mister.WriteCFG(ctx.CFGPath, ctx.CFGData); err != nil {
			send(map[string]interface{}{
				"mister":  "cfg_write",
				"success": false,
				"error":   err.Error(),
			})
			return
		}

		send(map[string]interface{}{
			"mister":      "cfg_write",
			"success":     true,
			"core_name":   ctx.OSD.CoreName,
			"option":      item.Name,
			"value":       req.Value,
			"value_index": valIdx,
			"cfg_path":    ctx.CFGPath,
			"source":      "cfg",
			"reload_required": true,
		})
		return
	}

	// Try DIP switches from MRA → write to .dip file
	if ctx.MRA != nil {
		dips := mister.ParseDIPSwitches(ctx.MRA)
		dip := mister.FindDIPSwitch(dips, req.Option)
		if dip != nil {
			valIdx := mister.FindDIPValue(dip, req.Value)
			if valIdx < 0 {
				send(map[string]interface{}{
					"mister":  "cfg_write",
					"success": false,
					"error":   fmt.Sprintf("value %q not found for DIP switch %s (available: %v)", req.Value, req.Option, dip.Values),
				})
				return
			}

			mister.SetBitRange(ctx.DIPData, dip.Bit, dip.BitHigh, valIdx)

			if err := mister.WriteDIP(ctx.DIPPath, ctx.DIPData); err != nil {
				send(map[string]interface{}{
					"mister":  "cfg_write",
					"success": false,
					"error":   err.Error(),
				})
				return
			}

			send(map[string]interface{}{
				"mister":      "cfg_write",
				"success":     true,
				"core_name":   ctx.OSD.CoreName,
				"option":      dip.Name,
				"value":       req.Value,
				"value_index": valIdx,
				"dip_path":    ctx.DIPPath,
				"source":      "dip",
				"reload_required": true,
			})
			return
		}
	}

	send(map[string]interface{}{
		"mister":  "cfg_write",
		"success": false,
		"error":   fmt.Sprintf("option not found: %s (checked CONF_STR options and DIP switches)", req.Option),
	})
}

// osdMaskFor decides what is known of the running core's OSD mask: the
// caller's osd_mask, else a core-specific inference (MSX1 with a per-build
// sidecar, so the rules match the build), else nothing.
func osdMaskFor(req Request, ctx *coreContext) (mister.OSDMask, map[string]interface{}) {
	if req.OSDMask != nil {
		m := mister.FullMask(*req.OSDMask)
		return m, map[string]interface{}{"value": m.Value, "known": m.Known, "source": "request"}
	}
	if ctx.OSD.CoreName == "MSX1" && ctx.OSDSource == "sidecar" {
		pack, err := mister.LoadMSX1MachinePack()
		m, notes := mister.InferMSX1Mask(ctx.CFGData, pack)
		info := map[string]interface{}{"value": m.Value, "known": m.Known, "source": "inferred-msx1", "notes": notes}
		if err != nil {
			info["pack_error"] = err.Error()
		} else {
			info["pack"] = pack
		}
		return m, info
	}
	return mister.OSDMask{}, map[string]interface{}{"source": "none"}
}


// handleMount loads or mounts a file through the core's OSD, without
// restarting the core: navigate to the F/S row named by Target, open its
// file browser and walk it to Path.  Needs log_file_entry=1 in MiSTer.ini
// so the browser can be followed (see mister.BrowseAndSelect).
func (s *Server) handleMount(req Request, send func(interface{})) {
	fail := func(msg string, extra map[string]interface{}) {
		r := map[string]interface{}{"mister": "mount", "success": false, "error": msg}
		for k, v := range extra {
			r[k] = v
		}
		send(r)
	}
	if req.Target == "" || req.Path == "" {
		fail("mount requires target (the OSD row, e.g. \"Mount Drive A:\") and path", nil)
		return
	}
	path := req.Path
	if !filepath.IsAbs(path) {
		path = filepath.Join("/media/fat", path)
	}
	if st, err := os.Stat(path); err != nil || st.IsDir() {
		fail(fmt.Sprintf("no such file: %s", path), nil)
		return
	}
	ctx, ok := s.resolveCore(req, send, true)
	if !ok {
		return
	}
	mask, maskInfo := osdMaskFor(req, ctx)
	loc, err := mister.FindOSDItemPositionKnown(ctx.OSD, req.Target, mask)
	if err != nil {
		fail(err.Error(), map[string]interface{}{"osd_mask": maskInfo})
		return
	}
	switch loc.Item.Type {
	case "file_load", "file_load_core", "mount":
	default:
		fail(fmt.Sprintf("%q is a %s row, not a file load or mount row", req.Target, loc.Item.Type), nil)
		return
	}
	mister.ClearBrowserState()
	if err := mister.OSDNavigateToOSD(ctx.OSD, mask, req.Target); err != nil {
		fail(err.Error(), map[string]interface{}{"osd_mask": maskInfo})
		return
	}
	if err := mister.PressKey("enter"); err != nil {
		fail(err.Error(), nil)
		return
	}
	time.Sleep(700 * time.Millisecond)
	st, err := mister.BrowseAndSelect(path)
	if err != nil {
		fail(err.Error(), map[string]interface{}{"browser": st, "osd_mask": maskInfo})
		return
	}
	send(map[string]interface{}{
		"mister":   "mount",
		"success":  true,
		"target":   req.Target,
		"row":      loc.Item.Raw,
		"path":     path,
		"browser":  st,
		"osd_mask": maskInfo,
	})
}


// handleMSX1Overlay takes a screenshot and decodes the MSX1 core's debug
// overlay rows.  With toggle, an overlay that is off is switched on for the
// reading (OSD "Debug Overlay" + Enter) and off again afterwards.
func (s *Server) handleMSX1Overlay(req Request, send func(interface{})) {
	fail := func(msg string) {
		send(map[string]interface{}{"mister": "msx1_overlay", "success": false, "error": msg})
	}
	shot := func() (*mister.MSX1Overlay, error) {
		res, err := mister.TakeScreenshotAndCapture(5 * time.Second)
		if err != nil {
			return nil, err
		}
		data, err := base64.StdEncoding.DecodeString(res.Data)
		if err != nil {
			return nil, err
		}
		return mister.DecodeMSX1Overlay(data)
	}
	ov, err := shot()
	toggled := false
	if err != nil && req.Toggle && strings.Contains(err.Error(), "no MSX1 debug overlay") {
		ctx, ok := s.resolveCore(req, send, true)
		if !ok {
			return
		}
		if ctx.OSD.CoreName != "MSX1" {
			fail("msx1_overlay is for the MSX1 core")
			return
		}
		flip := func() error {
			mask, _ := osdMaskFor(req, ctx)
			if err := mister.OSDNavigateToOSD(ctx.OSD, mask, "Debug Overlay"); err != nil {
				return err
			}
			for _, k := range []string{"enter", "f12"} {
				time.Sleep(400 * time.Millisecond)
				if err := mister.PressKey(k); err != nil {
					return err
				}
			}
			time.Sleep(800 * time.Millisecond)
			return nil
		}
		if err := flip(); err != nil {
			fail("turning Debug Overlay on: " + err.Error())
			return
		}
		toggled = true
		ov, err = shot()
		if ferr := flip(); ferr != nil {
			fail(fmt.Sprintf("read %v, but turning Debug Overlay off again failed: %v", err == nil, ferr))
			return
		}
	}
	if err != nil {
		fail(err.Error())
		return
	}
	send(map[string]interface{}{"mister": "msx1_overlay", "success": true, "toggled": toggled, "overlay": ov})
}
