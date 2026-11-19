package labs

import (
	"context"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"crucible/internal/apperr"
	"crucible/internal/auth"
)

// A terminal session outlives the websocket that opened it. A refresh, a closed browser tab, a walk back to the
// training page or a dropped connection reattaches to the same shell, with what it printed replayed, instead of
// starting a second one beside it: a serial console allows one connection per machine, and the one left behind held
// it. A session ends when the trainee closes the terminal, when its shell exits, or with the lab. One API replica
// (the agent hub and the setup locks are the same), so the registry is a map in memory.
type termSession struct {
	name    string // the lab.yaml terminal, for the transcript
	pty     PTY
	started time.Time

	mu     sync.Mutex
	rec    tail        // everything it printed: the transcript, and the replay for the next viewer
	viewer *termViewer // the websocket watching it now, nil between viewers
}

// termViewer is one websocket. write puts bytes on it; drop ends its handler, so the newest tab takes the terminal
// over rather than two tabs typing into one shell.
type termViewer struct {
	write func([]byte) error
	drop  context.CancelFunc
}

// attach replays the session onto v and makes it the viewer, dropping the one before it.
func (ts *termSession) attach(v *termViewer) {
	ts.mu.Lock()
	old := ts.viewer
	b, _ := ts.rec.Bytes()
	if len(b) > 0 {
		_ = v.write(b)
	}
	ts.viewer = v
	ts.mu.Unlock()
	if old != nil {
		old.drop()
	}
}

func (ts *termSession) detach(v *termViewer) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if ts.viewer == v {
		ts.viewer = nil
	}
}

// pump carries the shell's output to the viewer and the recording, and calls end when the shell is gone. The write
// happens under the lock so a replay can never land after output that followed it.
func (ts *termSession) pump(end func()) {
	defer end()
	buf := make([]byte, 32<<10)
	for {
		n, err := ts.pty.Read(buf)
		if n > 0 {
			ts.mu.Lock()
			ts.rec.Write(buf[:n])
			if ts.viewer != nil && ts.viewer.write(buf[:n]) != nil {
				ts.viewer = nil // a dead websocket; its handler detaches too
			}
			ts.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

func termKey(labID, tab string) string { return labID + "\x00" + tab }

// tabName is the terminal a tab belongs to: "console" and its copies ("console #2") share one lab.yaml entry, and
// nothing else is a tab. It bounds what a browser can open and keeps a key out of a transcript's terminal column.
var tabName = regexp.MustCompile(`^(.+?)(?: #([2-9]|[1-9][0-9]))?$`)

func tabTerminal(tab string) string {
	m := tabName.FindStringSubmatch(tab)
	if m == nil {
		return ""
	}
	return m[1]
}

// session returns the lab's session for this tab, opening the shell the first time. The shell outlives the request
// that opened it (context.WithoutCancel), so leaving the page does not kill it.
func (s *Service) session(ctx context.Context, inst *Instance, tab, name, service string, cols, rows int) (*termSession, error) {
	s.termMu.Lock()
	defer s.termMu.Unlock()
	key := termKey(inst.ID, tab)
	if ts := s.terms[key]; ts != nil {
		return ts, nil
	}
	rn, err := s.runner(inst.Runtime)
	if err != nil {
		return nil, err
	}
	pty, err := rn.OpenPTY(context.WithoutCancel(ctx), inst, service, cols, rows)
	if err != nil {
		return nil, err
	}
	ts := &termSession{name: name, pty: pty, started: s.Now()}
	if s.terms == nil {
		s.terms = map[string]*termSession{}
	}
	s.terms[key] = ts
	go ts.pump(func() { s.endSession(inst.ID, tab) })
	return ts, nil
}

// endSession closes the shell, drops the viewer's websocket and stores the transcript. Calling it twice is harmless.
func (s *Service) endSession(labID, tab string) {
	s.termMu.Lock()
	key := termKey(labID, tab)
	ts := s.terms[key]
	delete(s.terms, key)
	s.termMu.Unlock()
	if ts == nil {
		return
	}
	_ = ts.pty.Close()
	ts.mu.Lock()
	v := ts.viewer
	ts.viewer = nil
	ts.mu.Unlock()
	if v != nil {
		v.drop()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	s.saveTranscript(ctx, labID, ts.name, ts.started, &ts.rec) // the pump has stopped: rec has one reader now
}

// CloseTerminal ends one terminal of the trainee's own lab: the shell stops, the websocket watching it closes and the
// transcript is stored. It is how a serial console is given back (a machine allows one connection at a time).
func (s *Service) CloseTerminal(ctx context.Context, u *auth.User, labID, name, tab string) error {
	inst, err := s.owned(ctx, u, labID)
	if err != nil {
		return err
	}
	if tab == "" {
		tab = name
	}
	if tabTerminal(tab) != name {
		return apperr.Wrap(apperr.NotFound, "terminal not found")
	}
	s.endSession(inst.ID, tab)
	return nil
}

// openTabs lists the lab's live terminal tabs, so a browser that comes back opens the ones it left.
func (s *Service) openTabs(labID string) []string {
	s.termMu.Lock()
	defer s.termMu.Unlock()
	var tabs []string
	for key := range s.terms {
		if id, tab, ok := strings.Cut(key, "\x00"); ok && id == labID {
			tabs = append(tabs, tab)
		}
	}
	sort.Strings(tabs)
	return tabs
}

// endSessions closes every terminal of a lab that is going away.
func (s *Service) endSessions(labID string) {
	for _, tab := range s.openTabs(labID) {
		s.endSession(labID, tab)
	}
}
