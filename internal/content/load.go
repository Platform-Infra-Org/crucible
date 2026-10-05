package content

import (
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"crucible/internal/yamlx"
)

type loader struct {
	root  string
	probs []Problem
}

func (l *loader) add(path, format string, a ...any) {
	rel, err := filepath.Rel(l.root, path)
	if err != nil {
		rel = path
	}
	l.probs = append(l.probs, Problem{File: filepath.ToSlash(rel), Msg: fmt.Sprintf(format, a...)})
}

func (l *loader) read(path string, out any) bool {
	if err := yamlx.ReadFile(path, out, true); err != nil {
		l.add(path, "%v", err)
		return false
	}
	return true
}

// file checks that rel stays inside base and exists; it returns the joined path.
func (l *loader) file(base, rel, where string) (string, bool) {
	if !filepath.IsLocal(rel) {
		l.add(where, "path %q must stay inside %s", rel, filepath.Base(base))
		return "", false
	}
	p := filepath.Join(base, rel)
	if _, err := os.Stat(p); err != nil {
		l.add(p, "file not found")
		return "", false
	}
	return p, true
}

// Load parses and validates a content repo. It returns nil and the problems if anything is wrong.
func Load(dir string) (*Training, []Problem) {
	l := &loader{root: dir}
	l.noSymlinks(dir)
	tf := filepath.Join(dir, "training.yaml")
	t := &Training{Dir: dir}
	if !l.read(tf, t) {
		return nil, l.probs
	}
	if t.ID == "" {
		l.add(tf, "id is required")
	}
	if t.Title == "" {
		l.add(tf, "title is required")
	}
	if t.Progression == "" {
		t.Progression = "linear"
	}
	if t.Progression != "linear" && t.Progression != "free" {
		l.add(tf, "progression must be linear or free")
	}
	if len(t.ModuleIDs) == 0 {
		l.add(tf, "at least one module is required")
	}
	for i, m := range t.Maintainers {
		t.Maintainers[i] = strings.ToLower(m)
	}
	for _, id := range t.ModuleIDs {
		if !filepath.IsLocal(id) || strings.ContainsAny(id, `/\`) {
			l.add(tf, "invalid module id %q", id)
			continue
		}
		if m := l.module(filepath.Join(dir, "modules", id), id); m != nil {
			t.Modules = append(t.Modules, m)
		}
	}
	if len(l.probs) > 0 {
		return nil, l.probs
	}
	return t, nil
}

// noSymlinks rejects any symlink in the repo: every later read follows links, so one could expose server files.
func (l *loader) noSymlinks(dir string) {
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			l.add(p, "%v", err)
		case p == dir:
		case d.IsDir() && d.Name() == ".git":
			return filepath.SkipDir
		case d.Type()&fs.ModeSymlink != 0:
			l.add(p, "symlinks are not allowed in content repos")
		}
		return nil
	})
}

func (l *loader) module(dir, id string) *Module {
	mf := filepath.Join(dir, "module.yaml")
	m := &Module{ID: id, Dir: dir}
	if !l.read(mf, m) {
		return nil
	}
	if m.Title == "" {
		l.add(mf, "title is required")
	}
	if _, err := os.Stat(filepath.Join(dir, "quiz.yaml")); err == nil {
		m.Quiz = l.quiz(filepath.Join(dir, "quiz.yaml"))
	}
	seen := map[string]bool{}
	for _, raw := range m.RawItems {
		if len(raw) != 1 {
			l.add(mf, "each item must have exactly one of reading, quiz, lab")
			continue
		}
		for kind, rel := range raw {
			switch kind {
			case "reading":
				p, ok := l.file(dir, rel, mf)
				if !ok {
					continue
				}
				b, _ := os.ReadFile(p)
				itemID := strings.TrimSuffix(filepath.Base(rel), filepath.Ext(rel))
				if itemID == "quiz" || itemID == "lab" {
					l.add(mf, "reading id %q is reserved; rename %s", itemID, rel)
				}
				if seen[itemID] {
					l.add(mf, "duplicate reading id %q", itemID)
				}
				seen[itemID] = true
				m.Items = append(m.Items, Item{Kind: "reading", ID: itemID, Title: mdTitle(b, itemID), Path: p})
			case "quiz":
				if filepath.Clean(rel) != "quiz.yaml" || m.Quiz == nil {
					l.add(mf, "quiz item must point to an existing quiz.yaml")
					continue
				}
				if !hasStandaloneQuestions(m.Quiz) {
					l.add(mf, "quiz item needs at least one non-terminal question (terminal questions live in labs)")
				}
				m.Items = append(m.Items, Item{Kind: "quiz", ID: "quiz", Title: "Quiz", Path: filepath.Join(dir, "quiz.yaml")})
			case "lab":
				if filepath.Clean(rel) == "." {
					l.add(mf, "lab must be its own directory inside the module, not the module itself")
					continue
				}
				p, ok := l.file(dir, rel, mf)
				if !ok {
					continue
				}
				if m.Lab != nil {
					l.add(mf, "only one lab per module")
					continue
				}
				if m.Lab = l.lab(p, m.Quiz); m.Lab != nil {
					m.Items = append(m.Items, Item{Kind: "lab", ID: "lab", Title: "Lab", Path: p})
				}
			default:
				l.add(mf, "unknown item kind %q", kind)
			}
		}
	}
	if len(m.Items) == 0 {
		l.add(mf, "module has no items")
	}
	return m
}

func hasStandaloneQuestions(q *Quiz) bool {
	for _, x := range q.Questions {
		if x.Type != "terminal" {
			return true
		}
	}
	return false
}

func mdTitle(b []byte, fallback string) string {
	for _, line := range bytes.Split(b, []byte("\n")) {
		if s := strings.TrimSpace(string(line)); strings.HasPrefix(s, "# ") {
			return strings.TrimSpace(s[2:])
		}
	}
	return fallback
}

func (l *loader) quiz(path string) *Quiz {
	q := &Quiz{}
	if !l.read(path, q) {
		return nil
	}
	if q.PassThreshold == 0 {
		q.PassThreshold = 0.8
	}
	if q.PassThreshold < 0 || q.PassThreshold > 1 {
		l.add(path, "pass_threshold must be between 0 and 1")
	}
	ids := map[string]bool{}
	for i, x := range q.Questions {
		where := fmt.Sprintf("question %d (%s)", i+1, x.ID)
		bad := func(format string, a ...any) { l.add(path, where+": "+format, a...) }
		if x.ID == "" {
			bad("id is required")
		}
		if ids[x.ID] {
			bad("duplicate id")
		}
		ids[x.ID] = true
		if x.Prompt == "" {
			bad("prompt is required")
		}
		if x.Points == 0 {
			x.Points = 1
		}
		if x.Points < 0 {
			bad("points must be positive")
		}
		needAnswer := map[string]bool{"single": true, "multi": true, "exact": true, "regex": true}
		if needAnswer[x.Type] && x.Answer.Kind == 0 {
			bad("answer is required")
			continue
		}
		switch x.Type {
		case "single":
			var a int
			if len(x.Options) < 2 {
				bad("needs at least 2 options")
			} else if err := x.Answer.Decode(&a); err != nil || a < 0 || a >= len(x.Options) {
				bad("answer must be an option index (0-%d)", len(x.Options)-1)
			}
		case "multi":
			var a []int
			if len(x.Options) < 2 {
				bad("needs at least 2 options")
			} else if err := x.Answer.Decode(&a); err != nil || len(a) == 0 {
				bad("answer must be a list of option indexes")
			} else {
				for _, v := range a {
					if v < 0 || v >= len(x.Options) {
						bad("answer must be an option index (0-%d)", len(x.Options)-1)
					}
				}
			}
		case "exact":
			var s string
			if err := x.Answer.Decode(&s); err != nil || strings.TrimSpace(s) == "" {
				bad("answer must be a non-empty string")
			}
		case "regex":
			var s string
			if err := x.Answer.Decode(&s); err != nil {
				bad("answer must be a regex string")
			} else if _, err := regexp.Compile("^(?:" + s + ")$"); err != nil {
				bad("invalid regex: %v", err)
			}
		case "order":
			if len(x.Options) < 2 {
				bad("needs at least 2 options, listed in the correct order")
			}
		case "match":
			if len(x.Pairs) < 2 {
				bad("needs at least 2 pairs")
			}
			for _, p := range x.Pairs {
				if len(p) != 2 {
					bad("each pair must have exactly 2 entries")
				}
			}
		case "terminal":
			if x.Check == "" {
				bad("terminal questions need a check script")
			}
		case "text", "upload", "signoff":
		default:
			bad("unknown type %q", x.Type)
		}
	}
	return q
}

func (l *loader) lab(dir string, quiz *Quiz) *Lab {
	lf := filepath.Join(dir, "lab.yaml")
	lab := &Lab{Dir: dir}
	if !l.read(lf, lab) {
		return nil
	}
	if lab.ID == "" {
		l.add(lf, "id is required")
	}
	if lab.IdleTimeout > 0 && lab.IdleWarning > 0 && lab.IdleWarning >= lab.IdleTimeout {
		l.add(lf, "idle_warning must be shorter than idle_timeout")
	}
	if lab.IdleWarning == 0 {
		lab.IdleWarning = yamlx.Duration(5 * time.Minute)
	}
	if lab.TaskOrder == "" {
		lab.TaskOrder = "linear"
	}
	if lab.TaskOrder != "linear" && lab.TaskOrder != "free" {
		l.add(lf, "task_order must be linear or free")
	}
	if lab.HintCost < 0 {
		l.add(lf, "hint_cost must not be negative")
	}
	if lab.Compose == "" {
		lab.Compose = "compose.yaml"
	}

	services := map[string]bool{}
	switch lab.Runtime {
	case "local", "cluster":
		if p, ok := l.file(dir, lab.Compose, lf); ok {
			var c struct {
				Services map[string]yaml.Node `yaml:"services"`
			}
			if err := yamlx.ReadLoose(p, &c); err != nil {
				l.add(p, "%v", err)
			}
			for name := range c.Services {
				services[name] = true
			}
			if lab.Runtime == "local" {
				l.localCompose(p)
			}
		}
	case "aws":
		services["workspace"] = true // AWS labs get one workspace pod (spec §8.2)
	default:
		l.add(lf, "runtime must be cluster, local or aws")
	}

	if len(lab.Terminals) == 0 {
		l.add(lf, "at least one terminal is required")
	}
	names := map[string]bool{}
	for _, term := range lab.Terminals {
		if names[term.Name] {
			l.add(lf, "duplicate terminal name %q", term.Name)
		}
		names[term.Name] = true
		if !services[term.Service] {
			l.add(lf, "terminal %q: service %q is not defined in the lab", term.Name, term.Service)
		}
	}
	if lab.Setup != nil {
		l.script(dir, lf, "lab setup", lab.Setup, services, 60*time.Second)
	}
	if len(lab.Tasks) == 0 {
		l.add(lf, "at least one task is required")
	}
	taskIDs := map[string]bool{}
	for _, t := range lab.Tasks {
		where := "task " + t.ID
		if t.ID == "" {
			l.add(lf, "every task needs an id")
		}
		if taskIDs[t.ID] {
			l.add(lf, "duplicate task id %q", t.ID)
		}
		taskIDs[t.ID] = true
		if t.Instructions == "" {
			l.add(lf, "%s: instructions are required", where)
		} else {
			l.file(dir, t.Instructions, lf)
		}
		if t.Check != nil && t.Quiz != "" {
			l.add(lf, "%s: use either check or quiz, not both", where)
		}
		if t.Check == nil && t.Quiz == "" && !t.HumanReview {
			l.add(lf, "%s: needs a check, a quiz or human_review", where)
		}
		if t.Check != nil {
			l.script(dir, lf, where+" check", t.Check, services, 30*time.Second)
		}
		if t.Setup != nil {
			l.script(dir, lf, where+" setup", t.Setup, services, 60*time.Second)
		}
		if t.Quiz != "" {
			var q *Question
			if quiz != nil {
				q = quiz.Question(t.Quiz)
			}
			if q == nil || q.Type != "terminal" {
				l.add(lf, "%s: quiz %q must be a terminal question in the module's quiz.yaml", where, t.Quiz)
			} else {
				runIn := q.RunIn
				if runIn == "" && len(lab.Terminals) > 0 {
					runIn = lab.Terminals[0].Service
				}
				q.Script = &Script{Script: q.Check, RunIn: runIn}
				l.script(dir, lf, where+" quiz check", q.Script, services, 30*time.Second)
				if t.Points == 0 {
					t.Points = q.Points
				}
			}
		}
		if t.Points == 0 {
			t.Points = 1
		}
		if t.Points < 0 {
			l.add(lf, "%s: points must be positive", where)
		}
		for i, h := range t.Hints {
			if (h.Text == "") == (h.File == "") {
				l.add(lf, "%s: hint %d needs exactly one of text or file", where, i+1)
			}
			if h.File != "" {
				l.file(dir, h.File, lf)
			}
			if c := h.EffectiveCost(lab); c < 0 || c > t.Points {
				l.add(lf, "%s: hint %d cost %.2f must be between 0 and the task's %.2f points", where, i+1, c, t.Points)
			}
		}
	}
	return lab
}

func (l *loader) script(dir, lf, what string, s *Script, services map[string]bool, def time.Duration) {
	if s.Script == "" {
		l.add(lf, "%s: script is required", what)
		return
	}
	if p, ok := l.file(dir, s.Script, lf); ok {
		if st, err := os.Stat(p); err == nil && st.Mode()&0o111 == 0 {
			l.add(p, "must be executable (chmod +x)")
		}
	}
	switch {
	case s.RunIn == "":
		l.add(lf, "%s: run_in is required", what)
	case !services[s.RunIn]:
		l.add(lf, "%s: run_in %q is not a service in the lab", what, s.RunIn)
	}
	if s.Timeout == 0 {
		s.Timeout = yamlx.Duration(def)
	}
}

// localServiceKeys are the compose service settings a local lab may use. It is an allowlist: anything else
// (privileged, *_mode/pid/ipc/uts/cgroup, devices, cap_add, security_opt, ports, build, extra_hosts, secrets,
// configs, volumes_from, logging, …) could reach the trainee's laptop.
var localServiceKeys = map[string]bool{
	"image": true, "command": true, "entrypoint": true, "environment": true, "env_file": true, "working_dir": true,
	"user": true, "hostname": true, "expose": true, "volumes": true, "tmpfs": true, "healthcheck": true,
	"depends_on": true, "restart": true, "networks": true, "labels": true, "stop_signal": true,
	"stop_grace_period": true, "tty": true, "stdin_open": true, "init": true, "read_only": true, "cap_drop": true,
	"mem_limit": true, "cpus": true, "dns": true, "platform": true, "pull_policy": true,
}

// localPath reports whether p is a path inside the lab directory (no absolute, ~, $VAR or ../ escapes).
func localPath(p string) bool {
	return p != "" && !strings.ContainsAny(p, "~$") && filepath.IsLocal(filepath.Clean(p))
}

// localCompose rejects compose settings that would give a local lab access to the trainee's laptop.
func (l *loader) localCompose(file string) {
	raw, err := os.ReadFile(file)
	if err != nil {
		return // l.file in lab() already reported it
	}
	// docker compose merges every "---" document, so exactly one is allowed.
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	var top map[string]yaml.Node
	if err := dec.Decode(&top); err != nil {
		if err != io.EOF { // an empty file is reported by lab() as having no services
			l.add(file, "%v", err)
		}
		return
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); err != io.EOF {
		l.add(file, "compose file: multiple YAML documents are not allowed for runtime: local")
		return
	}
	bad := func(where, what string) {
		l.add(file, "%s: %s is not allowed for runtime: local (it can reach the trainee's laptop); use runtime: cluster", where, what)
	}
	declared := map[string]bool{}
	if vols, ok := top["volumes"]; ok {
		var defs map[string]yaml.Node
		_ = vols.Decode(&defs)
		for name := range defs {
			declared[name] = true
		}
	}
	for _, k := range slices.Sorted(maps.Keys(top)) {
		v := top[k]
		switch {
		case strings.HasPrefix(k, "x-"), k == "version", k == "name":
		case k == "services":
			var svcs map[string]yaml.Node
			if err := v.Decode(&svcs); err != nil {
				l.add(file, "services: %v", err)
				continue
			}
			for _, name := range slices.Sorted(maps.Keys(svcs)) {
				n := svcs[name]
				l.localService(file, name, &n, declared, bad)
			}
		case k == "volumes", k == "networks":
			var defs map[string]map[string]yaml.Node
			if err := v.Decode(&defs); err != nil {
				l.add(file, "%s: %v", k, err)
				continue
			}
			kind := strings.TrimSuffix(k, "s")
			for _, name := range slices.Sorted(maps.Keys(defs)) {
				for _, opt := range slices.Sorted(maps.Keys(defs[name])) {
					o := defs[name][opt]
					switch {
					case opt == "labels", kind == "network" && opt == "internal":
					case kind == "network" && opt == "driver" && o.Value == "bridge":
					default:
						bad(fmt.Sprintf("%s %q", kind, name), opt)
					}
				}
			}
		default:
			bad("compose file", k)
		}
	}
}

func (l *loader) localService(file, name string, n *yaml.Node, declared map[string]bool, bad func(where, what string)) {
	var svc map[string]yaml.Node
	if err := n.Decode(&svc); err != nil {
		l.add(file, "service %q: %v", name, err)
		return
	}
	where := fmt.Sprintf("service %q", name)
	for _, k := range slices.Sorted(maps.Keys(svc)) {
		v := svc[k]
		switch {
		case strings.HasPrefix(k, "x-"): // extensions; yaml.v3 resolves "<<" merges when decoding, so merged keys are checked too
		case k == "volumes":
			localVolumes(where, &v, declared, bad)
		case k == "env_file":
			for _, p := range envFiles(&v) {
				if !localPath(p) {
					bad(where, fmt.Sprintf("env_file %q (a host path outside the lab)", p))
				}
			}
		case !localServiceKeys[k]:
			bad(where, k)
		}
	}
}

var volumeModes = map[string]bool{"ro": true, "rw": true, "z": true, "Z": true, "nocopy": true, "delegated": true,
	"cached": true, "consistent": true, "shared": true, "slave": true, "private": true, "rshared": true, "rslave": true, "rprivate": true}

// localVolumes allows named volumes (declared at the top level), tmpfs, and read-only binds inside the lab
// directory. A writable bind would let the container plant symlinks or rewrite files the agent re-reads.
func localVolumes(where string, n *yaml.Node, declared map[string]bool, bad func(where, what string)) {
	if n.Kind != yaml.SequenceNode {
		bad(where, "volumes that are not a list")
		return
	}
	for _, v := range n.Content {
		var src string
		ro := false
		if v.Kind == yaml.ScalarNode {
			parts := strings.Split(v.Value, ":")
			if len(parts) == 1 {
				continue // anonymous volume
			}
			if last := parts[len(parts)-1]; len(parts) > 2 && func() bool {
				for _, m := range strings.Split(last, ",") {
					if !volumeModes[m] {
						return false
					}
				}
				return true
			}() {
				ro = slices.Contains(strings.Split(last, ","), "ro")
				parts = parts[:len(parts)-1]
			}
			if len(parts) != 2 { // also catches Windows-style "C:\\x:/y"
				bad(where, fmt.Sprintf("volume %q (unsupported syntax)", v.Value))
				continue
			}
			src = parts[0]
		} else {
			var long struct {
				Type     string `yaml:"type"`
				Source   string `yaml:"source"`
				ReadOnly bool   `yaml:"read_only"`
			}
			_ = v.Decode(&long)
			switch long.Type {
			case "tmpfs":
				continue
			case "volume":
				if long.Source != "" && !declared[long.Source] {
					bad(where, fmt.Sprintf("volume %q (not declared in top-level volumes)", long.Source))
				}
				continue
			case "bind":
				src, ro = long.Source, long.ReadOnly
			default:
				bad(where, fmt.Sprintf("volume type %q", long.Type))
				continue
			}
		}
		if declared[src] {
			continue
		}
		if !localPath(src) {
			bad(where, fmt.Sprintf("volume %q (a host path outside the lab)", src))
		} else if !ro {
			bad(where, fmt.Sprintf("writable bind %q (binds must be read-only; use a named volume for state)", src))
		}
	}
}

// envFiles lists env_file paths in any of compose's shapes: "a", ["a", …], [{path: a}, …].
func envFiles(n *yaml.Node) []string {
	switch n.Kind {
	case yaml.ScalarNode:
		return []string{n.Value}
	case yaml.SequenceNode:
		var out []string
		for _, it := range n.Content {
			if it.Kind == yaml.ScalarNode {
				out = append(out, it.Value)
				continue
			}
			var long struct {
				Path string `yaml:"path"`
			}
			_ = it.Decode(&long)
			out = append(out, long.Path)
		}
		return out
	}
	return []string{""} // malformed: rejected as a non-local path
}
