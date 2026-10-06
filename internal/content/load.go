package content

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	hcljson "github.com/hashicorp/hcl/v2/json"
	"github.com/zclconf/go-cty/cty"
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
	if t.EstimatedHours < 0 {
		l.add(tf, "estimated_hours must not be negative")
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
	l.links(dir)
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
	switch {
	case m.Completion == "":
		m.Completion = "all_items"
		if m.Threshold != 0 {
			l.add(mf, "threshold only applies to completion: score")
		}
	case m.Completion == "all_items" && m.Threshold != 0:
		l.add(mf, "threshold only applies to completion: score")
	case m.Completion == "score":
		if m.Threshold <= 0 || m.Threshold > 1 {
			l.add(mf, "completion: score needs a threshold between 0 and 1 (e.g. 0.7)")
		}
	case m.Completion != "all_items":
		l.add(mf, "completion must be all_items or score")
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
	if q.MaxAttempts < 0 {
		l.add(path, "max_attempts must not be negative (0 = unlimited)")
	}
	if q.Cooldown < 0 {
		l.add(path, "cooldown must not be negative")
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
		case "text", "upload":
			if strings.TrimSpace(x.Rubric) == "" {
				bad("needs a rubric: scorers grade against it")
			}
		case "signoff":
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
		services["workspace"] = true // AWS labs get one workspace container (spec §8.2)
		l.awsModule(dir, lf, lab)
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
		if t.HumanReview && (t.Check != nil || t.Quiz != "") {
			l.add(lf, "%s: human_review tasks are scored by a person: drop check and quiz", where)
		}
		if t.HumanReview && strings.TrimSpace(t.Rubric) == "" {
			l.add(lf, "%s: human_review tasks need a rubric for the scorer", where)
		}
		if !t.HumanReview && t.Rubric != "" {
			l.add(lf, "%s: rubric is only read for human_review tasks", where)
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
	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil {
		if err != io.EOF { // an empty file is reported by lab() as having no services
			l.add(file, "%v", err)
		}
		return
	}
	for {
		var extra yaml.Node
		err := dec.Decode(&extra)
		if err == io.EOF {
			break
		}
		if err != nil || !emptyDoc(&extra) {
			l.add(file, "compose file: multiple YAML documents are not allowed for runtime: local")
			return
		}
	}
	if interpolates(&doc) {
		l.add(file, "compose variable interpolation is not allowed in local labs (use $$ for a literal $)")
		return
	}
	var top map[string]yaml.Node
	if err := doc.Decode(&top); err != nil {
		l.add(file, "%v", err)
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

// emptyDoc reports whether n is a YAML document with no content (e.g. after a trailing "---").
func emptyDoc(n *yaml.Node) bool {
	return n.Kind == 0 || n.Kind == yaml.DocumentNode && (len(n.Content) == 0 || n.Content[0].Tag == "!!null" && n.Content[0].Value == "")
}

// interpolates reports whether any key or value contains a compose ${VAR}/$VAR reference (anything but $$).
// Compose would resolve it against the agent's environment, leaking paths and secrets.
func interpolates(n *yaml.Node) bool {
	if n.Kind == yaml.ScalarNode && strings.Contains(strings.ReplaceAll(n.Value, "$$", ""), "$") {
		return true
	}
	for _, c := range n.Content {
		if interpolates(c) {
			return true
		}
	}
	return false
}

var (
	awsRegionRe = regexp.MustCompile(`^[a-z]{2}(-[a-z]+)+-\d$`)
	// required_providers may only name HashiCorp's own providers: anything else is a downloaded binary on the runner
	hashicorpProvider = regexp.MustCompile(`^(registry\.terraform\.io/)?hashicorp/[a-z0-9-]+$`)
	// HashiCorp providers that read arbitrary host paths (local_file, local_sensitive_file) or run commands (external)
	tfBannedProviders = map[string]bool{"hashicorp/local": true, "hashicorp/external": true}
)

const (
	maxModuleBytes = 512 << 10 // the module travels in a ConfigMap (1 MiB, base64)
	maxTFFileBytes = 128 << 10
	maxTFDepth     = 64    // HCL's parsers recurse per nesting level; deep input overflows the stack (a fatal error)
	maxTFOpeners   = 4000  // splats and calls recurse too; a lab module needs a few hundred at most
	maxTFTokens    = 50000 // bounds all parser work per file; real lab files are a few thousand tokens
	maxTFUnaryRun  = 64    // !!!… and ---… recurse once per operator with no bracket to count
	maxTFFiles     = 200
)

// The parts of a terraform file awsModule looks at; PartialContent ignores the rest.
var (
	tfTopSchema = &hcl.BodySchema{Blocks: []hcl.BlockHeaderSchema{
		{Type: "provider", LabelNames: []string{"name"}}, {Type: "terraform"}, {Type: "module", LabelNames: []string{"name"}},
		{Type: "import"}, {Type: "resource", LabelNames: []string{"type", "name"}},
		{Type: "data", LabelNames: []string{"type", "name"}}, {Type: "ephemeral", LabelNames: []string{"type", "name"}},
		{Type: "action", LabelNames: []string{"type", "name"}}}}
	tfTerraformSchema = &hcl.BodySchema{Blocks: []hcl.BlockHeaderSchema{
		{Type: "backend", LabelNames: []string{"type"}}, {Type: "cloud"}, {Type: "required_providers"}}}
	tfModuleSchema = &hcl.BodySchema{Attributes: []hcl.AttributeSchema{{Name: "source"}}}
)

// awsModule checks an aws lab (spec §4.5, §8.2). Region and price ceiling are required. terraform/ holds a module
// Crucible runs as-is after adding its own provider, backend and variables, so the module must not declare provider,
// backend, cloud or import blocks nor ship tfvars or state; providers must be HashiCorp's; module sources must be local
// so neither infracost (in the API pod) nor terraform fetches code from the network. Every .tf and .tf.json is parsed
// with HCL: a regex cannot be made sound for this. This runs inside crucible-api on an author's push, so sizes are
// capped before anything is parsed, nesting depth is capped before the (recursive) parser runs, and no expression is
// ever evaluated: the values lint cares about must be literal strings. hashicorp/local and hashicorp/external (host
// file reads, arbitrary commands) are rejected outright; anything else the module does at plan/apply time (local-exec)
// is the runner sandbox's job, not lint's.
func (l *loader) awsModule(dir, lf string, lab *Lab) {
	if lab.AWS == nil || !awsRegionRe.MatchString(lab.AWS.Region) {
		l.add(lf, "aws.region is required for runtime: aws (e.g. eu-west-1)")
	}
	if lab.AWS == nil || lab.AWS.MaxHourlyUSD <= 0 {
		l.add(lf, "aws.max_hourly_usd must be set above 0 for runtime: aws")
	}
	root := filepath.Join(dir, "terraform")
	var tfs []string
	size, files, tooBig := int64(0), 0, false
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := strings.ToLower(d.Name())
		if d.IsDir() {
			if name == ".terraform" { // a pre-seeded provider/module cache
				l.add(p, "do not ship .terraform/: Crucible runs terraform init itself")
				return filepath.SkipDir
			}
			return nil
		}
		if info, err := d.Info(); err == nil {
			size += info.Size()
			if info.Size() > maxTFFileBytes {
				tooBig = true
				l.add(p, "%d KiB; keep each file under %d KiB", info.Size()>>10, maxTFFileBytes>>10)
			}
		}
		switch {
		case strings.HasSuffix(name, ".tfvars") || strings.HasSuffix(name, ".tfvars.json"):
			l.add(p, "do not ship %s: Crucible injects the lab's variables", d.Name())
		case strings.HasSuffix(name, ".tfstate") || strings.HasSuffix(name, ".tfstate.backup"):
			l.add(p, "do not ship %s: Crucible stores state per lab", d.Name())
		case strings.HasSuffix(name, ".tf") || strings.HasSuffix(name, ".tf.json"):
			tfs = append(tfs, p)
		}
		if files++; files > maxTFFiles {
			l.add(root, "more than %d files; keep the module smaller", maxTFFiles)
			tooBig = true
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		l.add(root, "%v", err)
	}
	if len(tfs) == 0 {
		l.add(lf, "runtime: aws needs a terraform/ directory with at least one .tf file")
	}
	if size > maxModuleBytes {
		l.add(lf, "terraform/ is %d KiB; keep it under %d KiB", size>>10, maxModuleBytes>>10)
		return
	}
	if tooBig {
		return
	}
	for _, p := range tfs {
		l.tfFile(root, p, strings.HasSuffix(strings.ToLower(p), ".json"))
	}
}

// tfFile applies awsModule's rules to one terraform file (override files included: they are plain .tf to terraform).
func (l *loader) tfFile(root, p string, isJSON bool) {
	b, err := os.ReadFile(p)
	if err != nil {
		l.add(p, "%v", err)
		return
	}
	if prob := tfShape(b, isJSON); prob != "" {
		l.add(p, "%s", prob)
		return
	}
	var f *hcl.File
	var diags hcl.Diagnostics
	if isJSON {
		f, diags = hcljson.Parse(b, filepath.Base(p))
	} else {
		f, diags = hclsyntax.ParseConfig(b, filepath.Base(p), hcl.InitialPos)
	}
	if diags.HasErrors() {
		l.add(p, "%v", diags)
		return
	}
	if isJSON {
		l.tfJSONFileReads(root, p, b)
	} else {
		l.tfFileReads(root, p, f.Body.(*hclsyntax.Body))
	}
	top, _, diags := f.Body.PartialContent(tfTopSchema)
	l.tfDiags(p, diags)
	for _, blk := range top.Blocks {
		switch blk.Type {
		case "provider":
			l.add(p, "do not declare provider blocks (%s): Crucible adds the aws provider with the lab's region and tags", blk.Labels[0])
		case "resource", "data", "ephemeral", "action":
			if prov, _, _ := strings.Cut(blk.Labels[0], "_"); tfBannedProviders["hashicorp/"+prov] {
				l.add(p, "%s %q: hashicorp/%s is not allowed (it reads host files or runs commands)", blk.Type, blk.Labels[0], prov)
			}
		case "import": // it could adopt another lab's resources into this lab's state
			l.add(p, "do not declare import blocks: a lab creates its own resources")
		case "terraform":
			tb, _, diags := blk.Body.PartialContent(tfTerraformSchema)
			l.tfDiags(p, diags)
			for _, sub := range tb.Blocks {
				switch sub.Type {
				case "backend", "cloud":
					l.add(p, "do not declare a backend or cloud block: Crucible stores state per lab")
				case "required_providers":
					attrs, diags := sub.Body.JustAttributes()
					l.tfDiags(p, diags)
					for name, a := range attrs {
						l.providerSource(p, b, name, a)
					}
				}
			}
		case "module":
			mb, _, diags := blk.Body.PartialContent(tfModuleSchema)
			l.tfDiags(p, diags)
			src, ok := "", true
			if a := mb.Attributes["source"]; a != nil {
				src, ok = tfLiteral(a.Expr, b)
			}
			switch {
			case !ok:
				l.add(p, "module source must be a literal string")
			case !localModule(src):
				l.add(p, "module source %q must be a local path (./…) inside terraform/", src)
			}
		}
	}
}

// tfFileFuncs are terraform's functions that read files. infracost evaluates the module inside crucible-api, so a
// read of the pod's own files (service account token, mounted config) could steer the price shown to trainees.
var tfFileFuncs = map[string]bool{"file": true, "filebase64": true, "filemd5": true, "filesha1": true,
	"filesha256": true, "filesha512": true, "filebase64sha256": true, "filebase64sha512": true, "fileexists": true,
	"fileset": true, "templatefile": true}

const tfFileReadRule = "file reads (%s) must name a literal relative path inside the module, e.g. \"${path.module}/user_data.sh\""

// tfSafePath is a relative path with no way out of the module copy: not absolute, no ~ (file() expands it), no ..
func tfSafePath(s string) bool {
	return s != "" && !strings.HasPrefix(s, "/") && !strings.HasPrefix(s, "~") && !strings.ContainsAny(s, "\\\x00") &&
		!slices.Contains(strings.Split(s, "/"), "..")
}

// tfFileReads finds every call to a file-reading function, anywhere in the file (all blocks, nested expressions,
// templates), and allows only literal paths: "a/b", "./a/b", "${path.module}/a/b", or path.module itself as fileset's
// directory; fileset's pattern must be a literal relative path too.
func (l *loader) tfFileReads(root, p string, node hclsyntax.Node) {
	_ = hclsyntax.VisitAll(node, func(n hclsyntax.Node) hcl.Diagnostics {
		call, name := tfFileCall(n)
		if call == nil {
			return nil
		}
		if len(call.Args) == 0 || call.ExpandFinal {
			l.add(p, tfFileReadRule, name)
			return nil
		}
		rel, viaModule, ok := tfPathArg(call.Args[0], name == "fileset")
		if ok && name == "fileset" {
			pat, isLit := tfLiteral(call.Args[1%len(call.Args)], nil)
			ok = len(call.Args) == 2 && isLit && tfSafePath(pat)
		}
		if !ok {
			l.add(p, tfFileReadRule, name)
		} else if name == "templatefile" {
			l.tfTemplate(root, p, rel, viaModule)
		}
		return nil
	})
}

// tfFileCall returns n and its function name (core::file is file) when n calls a file-reading function.
func tfFileCall(n hclsyntax.Node) (*hclsyntax.FunctionCallExpr, string) {
	call, ok := n.(*hclsyntax.FunctionCallExpr)
	if !ok {
		return nil, ""
	}
	name := call.Name
	if i := strings.LastIndex(name, "::"); i >= 0 {
		name = name[i+2:]
	}
	if !tfFileFuncs[name] {
		return nil, ""
	}
	return call, name
}

// tfPathArg reads a path argument: a literal relative path, or "${path.module}/<literal relative path>" (viaModule).
// dirOK also accepts path.module on its own.
func tfPathArg(e hclsyntax.Expression, dirOK bool) (rel string, viaModule, ok bool) {
	switch x := e.(type) {
	case *hclsyntax.ScopeTraversalExpr:
		return ".", true, dirOK && tfIsPathModule(x)
	case *hclsyntax.TemplateExpr:
		if s, lit := tfLiteral(x, nil); lit {
			return s, false, tfSafePath(s)
		}
		if len(x.Parts) != 2 {
			return "", false, false
		}
		mod, ok1 := x.Parts[0].(*hclsyntax.ScopeTraversalExpr)
		lit, ok2 := x.Parts[1].(*hclsyntax.LiteralValueExpr)
		if !ok1 || !ok2 || !tfIsPathModule(mod) || lit.Val.Type() != cty.String || lit.Val.IsNull() {
			return "", false, false
		}
		s, found := strings.CutPrefix(lit.Val.AsString(), "/")
		return s, true, found && tfSafePath(s)
	}
	return "", false, false
}

func tfIsPathModule(x *hclsyntax.ScopeTraversalExpr) bool {
	if len(x.Traversal) != 2 || x.Traversal.RootName() != "path" {
		return false
	}
	a, ok := x.Traversal[1].(hcl.TraverseAttr)
	return ok && a.Name == "module"
}

// tfJSONFileReads is tfFileReads for .tf.json, where expressions hide in strings (keys included, escapes decoded):
// every string is parsed as the template terraform would evaluate, after the same shape guard as a .tf file, and its
// calls are checked like native ones. Any string that is not a valid template is a problem (errs toward rejecting).
func (l *loader) tfJSONFileReads(root, p string, b []byte) {
	var v any
	if err := json.Unmarshal(b, &v); err != nil { // tfShape already required valid JSON
		l.add(p, "%v", err)
		return
	}
	check := func(s string) {
		if !strings.Contains(s, "${") && !strings.Contains(s, "%{") {
			return // no template sequence: a literal
		}
		if e := l.tfParseTemplate(p, []byte(s)); e != nil {
			l.tfFileReads(root, p, e)
		}
	}
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case string:
			check(x)
		case []any:
			for _, e := range x {
				walk(e)
			}
		case map[string]any:
			for k, e := range x {
				check(k)
				walk(e)
			}
		}
	}
	walk(v)
}

// tfParseTemplate parses b as an HCL template behind tfShape's guard (lexed first, depth/opener/token caps), reporting
// any problem against p; nil means it was reported.
func (l *loader) tfParseTemplate(p string, b []byte) hclsyntax.Expression {
	toks, diags := hclsyntax.LexTemplate(b, "", hcl.InitialPos)
	if diags.HasErrors() {
		l.add(p, "%v", diags)
		return nil
	}
	if prob := tfTokenShape(toks); prob != "" {
		l.add(p, "%s", prob)
		return nil
	}
	e, diags := hclsyntax.ParseTemplate(b, filepath.Base(p), hcl.InitialPos)
	if diags.HasErrors() {
		l.add(p, "%v", diags)
		return nil
	}
	return e
}

// tfTemplate checks a templatefile target: it is evaluated too, so it must be a small regular file in the module,
// parse as a template (behind the same guard as a .tf file) and call no file-reading function itself.
// A literal path is relative to terraform's working directory (root); ${path.module} to the calling file's directory.
func (l *loader) tfTemplate(root, p, rel string, viaModule bool) {
	base := root
	if viaModule {
		base = filepath.Dir(p)
	}
	t := filepath.Join(base, filepath.FromSlash(rel))
	info, err := os.Lstat(t)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxTFFileBytes {
		l.add(p, "templatefile %q must be a regular file in terraform/ under %d KiB", rel, maxTFFileBytes>>10)
		return
	}
	b, err := os.ReadFile(t)
	if err != nil || int64(len(b)) > maxTFFileBytes {
		l.add(p, "templatefile %q must be a regular file in terraform/ under %d KiB", rel, maxTFFileBytes>>10)
		return
	}
	e := l.tfParseTemplate(t, b)
	if e == nil {
		return
	}
	reads := false
	_ = hclsyntax.VisitAll(e, func(n hclsyntax.Node) hcl.Diagnostics {
		if call, _ := tfFileCall(n); call != nil {
			reads = true
		}
		return nil
	})
	if reads {
		l.add(p, "templatefile %q must not read files itself", rel)
	}
}

// providerSource checks one required_providers entry: a version string (implied hashicorp/<name>) or an object of
// literal strings whose source, if any, is hashicorp/<name> or registry.terraform.io/hashicorp/<name>.
func (l *loader) providerSource(p string, src []byte, name string, a *hcl.Attribute) {
	source := "hashicorp/" + strings.ToLower(name) // implied when no source is given
	defer func() {
		if tfBannedProviders[strings.TrimPrefix(strings.ToLower(source), "registry.terraform.io/")] {
			l.add(p, "required_providers entry %q: %s is not allowed (it reads host files or runs commands)", name, source)
		}
	}()
	if _, ok := tfLiteral(a.Expr, src); ok {
		return
	}
	kvs, diags := hcl.ExprMap(a.Expr)
	if diags.HasErrors() {
		l.add(p, "required_providers entry %q must be a literal string or object", name)
		return
	}
	for _, kv := range kvs {
		k, ok := tfLiteral(kv.Key, src)
		if !ok {
			k = tfIdent(kv.Key)
		}
		if k == "" {
			l.add(p, "required_providers entry %q: keys must be literal (a name or a plain string)", name)
			continue
		}
		v, ok := tfLiteral(kv.Value, src)
		if !ok {
			l.add(p, "required_providers entry %q: %q must be a literal string", name, k)
			continue
		}
		if k == "source" {
			source = v
			if !hashicorpProvider.MatchString(strings.ToLower(v)) {
				l.add(p, "provider source for %q must be hashicorp/<name> or registry.terraform.io/hashicorp/<name>", name)
			}
		}
	}
}

// tfIdent is a native object key written as a bare identifier (source = …), else "".
func tfIdent(e hcl.Expression) string {
	k, ok := e.(*hclsyntax.ObjectConsKeyExpr)
	if !ok || k.ForceNonLiteral {
		return ""
	}
	if t, ok := k.Wrapped.(*hclsyntax.ScopeTraversalExpr); ok && len(t.Traversal) == 1 {
		return t.Traversal.RootName()
	}
	return ""
}

// tfLiteral returns e's value when it is a literal string, without evaluating anything: a native "…" with no
// interpolation, or a JSON string (read from its source bytes; with a nil context HCL takes JSON strings literally).
func tfLiteral(e hcl.Expression, src []byte) (string, bool) {
	if k, ok := e.(*hclsyntax.ObjectConsKeyExpr); ok {
		e = k.Wrapped
	}
	switch x := e.(type) {
	case *hclsyntax.TemplateExpr:
		if len(x.Parts) == 1 {
			if lit, ok := x.Parts[0].(*hclsyntax.LiteralValueExpr); ok && lit.Val.Type() == cty.String && !lit.Val.IsNull() {
				return lit.Val.AsString(), true
			}
		}
		return "", false
	case hclsyntax.Expression: // any other native expression
		return "", false
	}
	r := e.Range()
	var s string
	if r.Start.Byte < 0 || r.End.Byte > len(src) || r.Start.Byte > r.End.Byte ||
		json.Unmarshal(src[r.Start.Byte:r.End.Byte], &s) != nil {
		return "", false
	}
	return s, true
}

// tfShape rejects, before the recursive HCL parser runs, any terraform file whose nesting could overflow its stack or
// whose bytes the parser would not see the way this check does. .tf: HCL's own lexer (a state machine); a lex error,
// a closer that does not match the innermost opener (hclsyntax recovers per line, so unmatched closers would let the
// openers that follow nest without bound), an unclosed opener, more than maxTFDepth levels or more than
// maxTFOpeners openers is a problem. .tf.json: it must be valid JSON to encoding/json (HCL's JSON scanner is more
// lenient and would keep going where encoding/json stops), and the same depth/opener caps apply. Strings are tokens
// to both, so brackets inside strings neither add nor hide depth.
func tfShape(b []byte, isJSON bool) string {
	depth, deepest, openers := 0, 0, 0
	open := func() {
		depth++
		openers++
		deepest = max(deepest, depth)
	}
	verdict := func() string { return tfVerdict(deepest, openers) }
	if isJSON {
		dec := json.NewDecoder(bytes.NewReader(b))
		for {
			tok, err := dec.Token()
			if err == io.EOF {
				if !json.Valid(b) { // e.g. a second top-level value, which the decoder takes as a stream
					return "invalid JSON"
				}
				return verdict()
			}
			if err != nil {
				return "invalid JSON: " + err.Error()
			}
			switch tok {
			case json.Delim('['), json.Delim('{'):
				open()
			case json.Delim(']'), json.Delim('}'):
				depth--
			}
			if deepest > maxTFDepth || openers > maxTFOpeners {
				return verdict()
			}
		}
	}
	toks, diags := hclsyntax.LexConfig(b, "", hcl.InitialPos)
	if diags.HasErrors() {
		return diags.Error()
	}
	return tfTokenShape(toks)
}

func tfVerdict(deepest, openers int) string {
	switch {
	case deepest > maxTFDepth:
		return fmt.Sprintf("nested too deeply (%d levels; keep it under %d)", deepest, maxTFDepth)
	case openers > maxTFOpeners:
		return fmt.Sprintf("too many brackets (%d; keep it under %d)", openers, maxTFOpeners)
	}
	return ""
}

// tfTokenShape is tfShape's check of lexed HCL (a .tf file, or a template from .tf.json or templatefile).
func tfTokenShape(toks hclsyntax.Tokens) string {
	depth, deepest, openers := 0, 0, 0
	open := func() {
		depth++
		openers++
		deepest = max(deepest, depth)
	}
	verdict := func() string { return tfVerdict(deepest, openers) }
	closes := map[hclsyntax.TokenType]hclsyntax.TokenType{
		hclsyntax.TokenOBrace: hclsyntax.TokenCBrace, hclsyntax.TokenOBrack: hclsyntax.TokenCBrack,
		hclsyntax.TokenOParen: hclsyntax.TokenCParen, hclsyntax.TokenOQuote: hclsyntax.TokenCQuote,
		hclsyntax.TokenOHeredoc: hclsyntax.TokenCHeredoc, hclsyntax.TokenTemplateInterp: hclsyntax.TokenTemplateSeqEnd,
		hclsyntax.TokenTemplateControl: hclsyntax.TokenTemplateSeqEnd,
	}
	isCloser := map[hclsyntax.TokenType]bool{}
	for _, c := range closes {
		isCloser[c] = true
	}
	var stack []hclsyntax.TokenType // expected closers, innermost last
	run := 0                        // consecutive ! and - tokens
	for _, t := range toks {
		if t.Type == hclsyntax.TokenBang || t.Type == hclsyntax.TokenMinus {
			if run++; run > maxTFUnaryRun {
				return fmt.Sprintf("too many operators in a row at line %d (keep it under %d)", t.Range.Start.Line, maxTFUnaryRun)
			}
		} else {
			run = 0
		}
		if c, ok := closes[t.Type]; ok {
			stack = append(stack, c)
			open()
			if v := verdict(); v != "" {
				return v
			}
		} else if isCloser[t.Type] {
			if len(stack) == 0 || stack[len(stack)-1] != t.Type {
				return fmt.Sprintf("unbalanced brackets at line %d", t.Range.Start.Line)
			}
			stack = stack[:len(stack)-1]
			depth--
		}
	}
	if len(stack) > 0 {
		return "unbalanced brackets: unclosed at end of file"
	}
	if len(toks) > maxTFTokens { // checked last so the specific problems above win; lexing is bounded by the size cap
		return fmt.Sprintf("too many tokens (%d; keep it under %d)", len(toks), maxTFTokens)
	}
	return ""
}

// localModule: ./ prefix, no .. element after cleaning (so it stays inside terraform/), no backslashes.
func localModule(s string) bool {
	return strings.HasPrefix(s, "./") && !strings.Contains(s, `\`) &&
		!slices.Contains(strings.Split(path.Clean(s), "/"), "..")
}

func (l *loader) tfDiags(p string, diags hcl.Diagnostics) {
	if diags.HasErrors() {
		l.add(p, "%v", diags)
	}
}

var (
	mdLink  = regexp.MustCompile(`!?\[[^\]]*\]\(\s*<?([^)\s>]+)`)
	mdFence = regexp.MustCompile("(?ms)^(```|~~~).*?^(```|~~~)")
	mdCode  = regexp.MustCompile("`[^`\\n]*`")
)

// links checks Markdown links and images under modules/ (spec §6 "broken links/assets"). assets/... must exist under
// the repo's assets/ with a type the app serves; other relative targets can't resolve inside Crucible. Absolute paths,
// URLs, mailto: and #anchors are external and skipped (nothing is fetched). Fenced blocks and inline code spans are skipped.
// ponytail: a regex scan, not a Markdown parser.
func (l *loader) links(dir string) {
	_ = filepath.WalkDir(filepath.Join(dir, "modules"), func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.EqualFold(filepath.Ext(p), ".md") {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		for _, m := range mdLink.FindAllSubmatch(mdCode.ReplaceAll(mdFence.ReplaceAll(b, nil), nil), -1) {
			target := string(m[1])
			switch {
			case strings.HasPrefix(target, "#"), strings.HasPrefix(target, "/"), strings.Contains(target, "://"), strings.HasPrefix(target, "mailto:"):
			case strings.HasPrefix(target, "assets/"):
				rel, _, _ := strings.Cut(strings.TrimPrefix(target, "assets/"), "#")
				rel, _, _ = strings.Cut(rel, "?")
				if !filepath.IsLocal(filepath.FromSlash(rel)) {
					l.add(p, "asset link %q leaves assets/", target)
					continue
				}
				fp := filepath.Join(dir, "assets", filepath.FromSlash(rel))
				if fi, err := os.Stat(fp); err != nil || !fi.Mode().IsRegular() {
					l.add(p, "broken asset link %q", target)
				} else if !AssetTypes[strings.ToLower(filepath.Ext(fp))] {
					l.add(p, "asset %q is not an image or font Crucible serves", target)
				}
			default:
				l.add(p, "relative link %q won't resolve in Crucible; use assets/... or a full URL", target)
			}
		}
		return nil
	})
}
