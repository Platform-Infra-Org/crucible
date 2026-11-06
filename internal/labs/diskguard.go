package labs

import (
	"context"
	"syscall"
	"time"
)

const (
	workspaceLimitMiB = 20 * 1024 // the workspace volume's size; local-path enforces none, so Crucible measures
	workspaceEvery    = 5 * time.Minute
	diskFullMsg       = "The forge's disk is nearly full: try again later, or ask an admin."
)

// minNodeFree is the node disk free space below which no new aws lab starts (a var for tests).
var minNodeFree = int64(15 << 30)

// workspaceMeter measures a lab's workspace volume (AWSRunner.WorkspaceMiB).
type workspaceMeter interface {
	WorkspaceMiB(ctx context.Context, inst *Instance) (int64, error)
}

// nodeFull reports whether the node's lab storage (CRUCIBLE_NODE_STORAGE, an empty directory on local-path's filesystem, mounted read-only)
// has less than minNodeFree left. Unreadable counts as full: the operator set the path, so a wrong one must show.
func (s *Service) nodeFull() bool {
	if s.NodeStorage == "" {
		return false
	}
	var st syscall.Statfs_t
	if err := syscall.Statfs(s.NodeStorage, &st); err != nil {
		s.Log.Error("reading the node's free disk failed", "path", s.NodeStorage, "err", err)
		return true
	}
	return int64(st.Bavail)*int64(st.Bsize) < minNodeFree
}

// guardWorkspaces (each sweep) measures every ready aws lab's workspace at most every workspaceEvery: over the limit it
// is warned once, and still over at the next measure the lab is put out.
func (s *Service) guardWorkspaces(ctx context.Context) {
	wm, ok := s.Runners["aws"].(workspaceMeter)
	if !ok {
		return
	}
	rows, err := s.DB.Query(ctx, `SELECT `+instCols+` FROM lab_instances WHERE runtime = 'aws' AND state = 'ready'`)
	if err != nil {
		s.Log.Error("workspace guard query failed", "err", err)
		return
	}
	labs, err := collectInst(rows)
	if err != nil {
		s.Log.Error("workspace guard query failed", "err", err)
		return
	}
	now := s.Now()
	for _, inst := range labs {
		s.measureMu.Lock()
		if s.measuredAt == nil {
			s.measuredAt = map[string]time.Time{}
		}
		last, seen := s.measuredAt[inst.ID]
		if seen && now.Sub(last) < workspaceEvery {
			s.measureMu.Unlock()
			continue
		}
		s.measuredAt[inst.ID] = now
		s.measureMu.Unlock()
		s.once("measure:"+inst.ID, func() { // a slow du never holds the sweep
			ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), workspaceEvery)
			defer cancel()
			s.measure(ctx, wm, inst)
		})
	}
}

func (s *Service) measure(ctx context.Context, wm workspaceMeter, inst *Instance) {
	mib, err := wm.WorkspaceMiB(ctx, inst)
	if err != nil {
		s.Log.Warn("measuring a workspace failed", "lab", inst.ID, "err", err)
		return
	}
	if mib <= workspaceLimitMiB {
		return
	}
	var warned bool
	if err := s.DB.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM lab_events WHERE lab_id = $1 AND kind = 'workspace_full_warning')`,
		inst.ID).Scan(&warned); err != nil {
		return
	}
	if !warned {
		s.event(ctx, inst.ID, "workspace_full_warning", "")
		return
	}
	s.destroy(ctx, inst, "workspace_full")
}
