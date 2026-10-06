package content

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// base is a minimal valid training. Tests override single files to break it.
var base = map[string]string{
	"training.yaml":               "id: t1\ntitle: T1\nmodules: [m1]\n",
	"modules/m1/module.yaml":      "title: M1\nitems:\n  - reading: reading/intro.md\n  - quiz: quiz.yaml\n  - lab: lab\n",
	"modules/m1/reading/intro.md": "# Intro to the Forge\nHello.\n",
	"modules/m1/quiz.yaml": `questions:
  - {id: q1, type: single, prompt: P, options: [a, b], answer: 1}
  - {id: q2, type: terminal, prompt: "Port?", check: checks/q2.sh}
`,
	"modules/m1/lab/lab.yaml": `id: l1
runtime: local
terminals: [{name: shell, service: box}]
tasks:
  - id: t1
    instructions: tasks/t1.md
    check: {script: checks/t1.sh, run_in: box}
    points: 2
    hints:
      - text: nudge
      - file: hints/sol.md
        cost: 1
  - id: t2
    instructions: tasks/t2.md
    setup: {script: setup/t2.sh, run_in: box}
    quiz: q2
`,
	"modules/m1/lab/compose.yaml": "services:\n  box:\n    image: alpine:3.22\n",
	"modules/m1/lab/tasks/t1.md":  "Do t1",
	"modules/m1/lab/tasks/t2.md":  "Do t2",
	"modules/m1/lab/checks/t1.sh": "#!/bin/sh\nexit 0\n",
	"modules/m1/lab/checks/q2.sh": "#!/bin/sh\nexit 0\n",
	"modules/m1/lab/setup/t2.sh":  "#!/bin/sh\nexit 0\n",
	"modules/m1/lab/hints/sol.md": "the answer",
}

func tree(t *testing.T, override map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{}
	for k, v := range base {
		files[k] = v
	}
	for k, v := range override {
		files[k] = v
	}
	for name, body := range files {
		if body == "<delete>" {
			continue
		}
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		mode := os.FileMode(0o644)
		if strings.HasSuffix(name, ".sh") && body != "<noexec>" {
			mode = 0o755
		}
		if err := os.WriteFile(p, []byte(body), mode); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestLoadMinimalAppliesDefaults(t *testing.T) {
	tr, probs := Load(tree(t, nil))
	if len(probs) > 0 {
		t.Fatalf("unexpected problems: %v", probs)
	}
	if tr.Progression != "linear" {
		t.Errorf("progression default = %q", tr.Progression)
	}
	m := tr.Module("m1")
	if m.Items[0].Title != "Intro to the Forge" || m.Items[0].ID != "intro" {
		t.Errorf("reading item = %+v", m.Items[0])
	}
	if m.Quiz.PassThreshold != 0.8 || m.Quiz.Question("q1").Points != 1 {
		t.Errorf("quiz defaults: %+v", m.Quiz)
	}
	lab := m.Lab
	if lab.IdleWarning.D() != 5*time.Minute || lab.TaskOrder != "linear" || lab.Compose != "compose.yaml" {
		t.Errorf("lab defaults: %+v", lab)
	}
	t1 := lab.Task("t1")
	if t1.Check.Timeout.D() != 30*time.Second {
		t.Errorf("check timeout default = %v", t1.Check.Timeout.D())
	}
	if t1.Hints[0].EffectiveCost(lab) != 0 || t1.Hints[1].EffectiveCost(lab) != 1 {
		t.Errorf("hint costs wrong")
	}
	t2 := lab.Task("t2")
	if t2.Setup.Timeout.D() != 60*time.Second || t2.Points != 1 {
		t.Errorf("t2 = %+v", t2)
	}
	q2 := m.Quiz.Question("q2")
	if q2.Script == nil || q2.Script.RunIn != "box" {
		t.Errorf("terminal question script not resolved: %+v", q2.Script)
	}
}

func TestLoadProblems(t *testing.T) {
	cases := map[string]struct {
		override map[string]string
		want     string
	}{
		"text needs rubric":            {map[string]string{"modules/m1/quiz.yaml": "questions:\n  - {id: q1, type: text, prompt: \"Why?\"}\n  - {id: q2, type: terminal, prompt: X, check: checks/q2.sh}\n"}, "needs a rubric"},
		"upload needs rubric":          {map[string]string{"modules/m1/quiz.yaml": "questions:\n  - {id: q1, type: upload, prompt: Attach}\n  - {id: q2, type: terminal, prompt: X, check: checks/q2.sh}\n"}, "needs a rubric"},
		"review needs rubric":          {map[string]string{"modules/m1/lab/lab.yaml": strings.Replace(base["modules/m1/lab/lab.yaml"], "    check: {script: checks/t1.sh, run_in: box}\n", "    human_review: true\n", 1)}, "human_review tasks need a rubric"},
		"review with check":            {map[string]string{"modules/m1/lab/lab.yaml": strings.Replace(base["modules/m1/lab/lab.yaml"], "    points: 2\n", "    points: 2\n    human_review: true\n    rubric: R\n", 1)}, "scored by a person: drop check and quiz"},
		"rubric on a checked task":     {map[string]string{"modules/m1/lab/lab.yaml": strings.Replace(base["modules/m1/lab/lab.yaml"], "    points: 2\n", "    points: 2\n    rubric: R\n", 1)}, "rubric is only read for human_review tasks"},
		"missing title":                {map[string]string{"training.yaml": "id: t1\nmodules: [m1]\n"}, "title is required"},
		"bad runtime":                  {map[string]string{"modules/m1/lab/lab.yaml": strings.Replace(base["modules/m1/lab/lab.yaml"], "local", "moon", 1)}, "runtime must be"},
		"unknown service":              {map[string]string{"modules/m1/lab/lab.yaml": strings.Replace(base["modules/m1/lab/lab.yaml"], "service: box", "service: nope", 1)}, `service "nope"`},
		"not executable":               {map[string]string{"modules/m1/lab/checks/t1.sh": "<noexec>"}, "must be executable"},
		"idle warning":                 {map[string]string{"modules/m1/lab/lab.yaml": "idle_timeout: 5m\nidle_warning: 5m\n" + base["modules/m1/lab/lab.yaml"]}, "idle_warning must be shorter"},
		"answer range":                 {map[string]string{"modules/m1/quiz.yaml": "questions:\n  - {id: q1, type: single, prompt: P, options: [a, b], answer: 7}\n  - {id: q2, type: terminal, prompt: X, check: checks/q2.sh}\n"}, "answer must be an option index"},
		"missing answer":               {map[string]string{"modules/m1/quiz.yaml": "questions:\n  - {id: q1, type: single, prompt: P, options: [a, b]}\n  - {id: q2, type: terminal, prompt: X, check: checks/q2.sh}\n"}, "answer is required"},
		"quiz not terminal":            {map[string]string{"modules/m1/lab/lab.yaml": strings.Replace(base["modules/m1/lab/lab.yaml"], "quiz: q2", "quiz: q1", 1)}, "terminal question"},
		"hint too costly":              {map[string]string{"modules/m1/lab/lab.yaml": strings.Replace(base["modules/m1/lab/lab.yaml"], "cost: 1", "cost: 5", 1)}, "hint 2 cost"},
		"path escape":                  {map[string]string{"modules/m1/module.yaml": "title: M1\nitems:\n  - reading: ../../../etc/passwd\n"}, "must stay inside"},
		"reading id quiz":              {map[string]string{"modules/m1/module.yaml": "title: M1\nitems:\n  - reading: reading/quiz.md\n", "modules/m1/reading/quiz.md": "# Q\n"}, `reading id "quiz" is reserved`},
		"reading id lab":               {map[string]string{"modules/m1/module.yaml": "title: M1\nitems:\n  - reading: lab.md\n", "modules/m1/lab.md": "# L\n"}, `reading id "lab" is reserved`},
		"lab is module dir":            {map[string]string{"modules/m1/module.yaml": "title: M1\nitems:\n  - lab: .\n", "modules/m1/lab.yaml": base["modules/m1/lab/lab.yaml"]}, "lab must be its own directory"},
		"local privileged":             {map[string]string{"modules/m1/lab/compose.yaml": "services:\n  box:\n    image: alpine:3.22\n    privileged: true\n"}, "privileged"},
		"local host net":               {map[string]string{"modules/m1/lab/compose.yaml": "services:\n  box:\n    image: alpine:3.22\n    network_mode: host\n"}, "network_mode is not allowed"},
		"local pid host":               {map[string]string{"modules/m1/lab/compose.yaml": "services:\n  box:\n    image: alpine:3.22\n    pid: host\n"}, "pid is not allowed"},
		"local ipc host":               {map[string]string{"modules/m1/lab/compose.yaml": "services:\n  box:\n    image: alpine:3.22\n    ipc: host\n"}, "ipc is not allowed"},
		"local cap_add":                {map[string]string{"modules/m1/lab/compose.yaml": "services:\n  box:\n    image: alpine:3.22\n    cap_add: [SYS_ADMIN]\n"}, "cap_add"},
		"local devices":                {map[string]string{"modules/m1/lab/compose.yaml": "services:\n  box:\n    image: alpine:3.22\n    devices: [/dev/kvm]\n"}, "devices"},
		"local abs bind":               {map[string]string{"modules/m1/lab/compose.yaml": "services:\n  box:\n    image: alpine:3.22\n    volumes: [\"/etc:/host-etc\"]\n"}, "host path"},
		"local home bind":              {map[string]string{"modules/m1/lab/compose.yaml": "services:\n  box:\n    image: alpine:3.22\n    volumes: [\"~/.ssh:/k\"]\n"}, "host path"},
		"local long bind":              {map[string]string{"modules/m1/lab/compose.yaml": "services:\n  box:\n    image: alpine:3.22\n    volumes: [{type: bind, source: /var/run/docker.sock, target: /s}]\n"}, "host path"},
		"local escape bind":            {map[string]string{"modules/m1/lab/compose.yaml": "services:\n  box:\n    image: alpine:3.22\n    volumes: [\"../../..:/up\"]\n"}, "host path"},
		"local security_opt":           {map[string]string{"modules/m1/lab/compose.yaml": "services:\n  box:\n    image: alpine:3.22\n    security_opt: [\"seccomp:unconfined\"]\n"}, "security_opt"},
		"local userns":                 {map[string]string{"modules/m1/lab/compose.yaml": "services:\n  box:\n    image: alpine:3.22\n    userns_mode: host\n"}, "userns_mode"},
		"local uts":                    {map[string]string{"modules/m1/lab/compose.yaml": "services:\n  box:\n    image: alpine:3.22\n    uts: host\n"}, "uts"},
		"local cgroup":                 {map[string]string{"modules/m1/lab/compose.yaml": "services:\n  box:\n    image: alpine:3.22\n    cgroup: host\n"}, "cgroup"},
		"local ports":                  {map[string]string{"modules/m1/lab/compose.yaml": "services:\n  box:\n    image: alpine:3.22\n    ports: [\"80:80\"]\n"}, "ports"},
		"local build":                  {map[string]string{"modules/m1/lab/compose.yaml": "services:\n  box:\n    build: /\n"}, "build"},
		"local volumes_from":           {map[string]string{"modules/m1/lab/compose.yaml": "services:\n  box:\n    image: alpine:3.22\n    volumes_from: [other]\n"}, "volumes_from"},
		"local extra_hosts":            {map[string]string{"modules/m1/lab/compose.yaml": "services:\n  box:\n    image: alpine:3.22\n    extra_hosts: [\"h:host-gateway\"]\n"}, "extra_hosts"},
		"local npipe volume":           {map[string]string{"modules/m1/lab/compose.yaml": "services:\n  box:\n    image: alpine:3.22\n    volumes: [{type: npipe, source: x, target: /x}]\n"}, `volume type "npipe"`},
		"local env_file abs":           {map[string]string{"modules/m1/lab/compose.yaml": "services:\n  box:\n    image: alpine:3.22\n    env_file: /etc/environment\n"}, "env_file"},
		"local env_file home":          {map[string]string{"modules/m1/lab/compose.yaml": "services:\n  box:\n    image: alpine:3.22\n    env_file: [\"~/.aws/credentials\"]\n"}, "env_file"},
		"local env_file long":          {map[string]string{"modules/m1/lab/compose.yaml": "services:\n  box:\n    image: alpine:3.22\n    env_file: [{path: ../../x}]\n"}, "env_file"},
		"top secrets":                  {map[string]string{"modules/m1/lab/compose.yaml": "services:\n  box:\n    image: alpine:3.22\nsecrets:\n  k:\n    file: /etc/shadow\n"}, "secrets is not allowed"},
		"top configs":                  {map[string]string{"modules/m1/lab/compose.yaml": "services:\n  box:\n    image: alpine:3.22\nconfigs:\n  c:\n    file: /etc/hosts\n"}, "configs is not allowed"},
		"top include":                  {map[string]string{"modules/m1/lab/compose.yaml": "include: [/etc/x.yaml]\nservices:\n  box:\n    image: alpine:3.22\n"}, "include is not allowed"},
		"volume driver_opts":           {map[string]string{"modules/m1/lab/compose.yaml": "services:\n  box:\n    image: alpine:3.22\nvolumes:\n  data:\n    driver_opts: {type: none, o: bind, device: /}\n"}, "driver_opts"},
		"external volume":              {map[string]string{"modules/m1/lab/compose.yaml": "services:\n  box:\n    image: alpine:3.22\nvolumes:\n  data:\n    external: true\n"}, "external"},
		"host network driver":          {map[string]string{"modules/m1/lab/compose.yaml": "services:\n  box:\n    image: alpine:3.22\nnetworks:\n  n:\n    driver: host\n"}, `network "n": driver`},
		"merged privileged":            {map[string]string{"modules/m1/lab/compose.yaml": "x-c: &c {image: alpine:3.22, privileged: true}\nservices:\n  box:\n    <<: *c\n"}, "privileged"},
		"service not a map":            {map[string]string{"modules/m1/lab/compose.yaml": "services:\n  box: [image]\n"}, "box"},
		"volumes not a list":           {map[string]string{"modules/m1/lab/compose.yaml": "services:\n  box:\n    image: alpine:3.22\n    volumes: /:/h\n"}, "volumes that are not a list"},
		"bind via $VAR":                {map[string]string{"modules/m1/lab/compose.yaml": "services:\n  box:\n    image: alpine:3.22\n    volumes: [\"${HOME}:/h\"]\n"}, "interpolation is not allowed"},
		"multi-document":               {map[string]string{"modules/m1/lab/compose.yaml": "services:\n  box:\n    image: alpine:3.22\n---\nservices:\n  box:\n    privileged: true\n    volumes: [\"/:/host:ro\"]\n"}, "multiple YAML documents"},
		"multi-document empty first":   {map[string]string{"modules/m1/lab/compose.yaml": "---\n---\nservices:\n  box:\n    privileged: true\n"}, "multiple YAML documents"},
		"rw short bind":                {map[string]string{"modules/m1/lab/compose.yaml": "services:\n  box:\n    image: alpine:3.22\n    volumes: [\"./files:/files\"]\n"}, "writable bind"},
		"rw short bind explicit":       {map[string]string{"modules/m1/lab/compose.yaml": "services:\n  box:\n    image: alpine:3.22\n    volumes: [\"./files:/files:rw\"]\n"}, "writable bind"},
		"rw long bind":                 {map[string]string{"modules/m1/lab/compose.yaml": "services:\n  box:\n    image: alpine:3.22\n    volumes: [{type: bind, source: ./files, target: /f}]\n"}, "writable bind"},
		"rw long bind false":           {map[string]string{"modules/m1/lab/compose.yaml": "services:\n  box:\n    image: alpine:3.22\n    volumes: [{type: bind, source: ./files, target: /f, read_only: false}]\n"}, "writable bind"},
		"windows drive bind":           {map[string]string{"modules/m1/lab/compose.yaml": "services:\n  box:\n    image: alpine:3.22\n    volumes: [\"C:\\\\:/host:ro\"]\n"}, "unsupported syntax"},
		"undeclared named volume long": {map[string]string{"modules/m1/lab/compose.yaml": "services:\n  box:\n    image: alpine:3.22\n    volumes: [{type: volume, source: nope, target: /d}]\n"}, "not declared"},
		"quoted merge key":             {map[string]string{"modules/m1/lab/compose.yaml": "services:\n  box:\n    image: alpine:3.22\n    \"<<\": {privileged: true}\n"}, "<<"},
		"bind ro up":                   {map[string]string{"modules/m1/lab/compose.yaml": "services:\n  box:\n    image: alpine:3.22\n    volumes: [\"../x:/x:ro\"]\n"}, "host path"},
		"interp PATH env":              {map[string]string{"modules/m1/lab/compose.yaml": "services:\n  box:\n    image: alpine:3.22\n    environment: [\"X=${PATH}\"]\n"}, "interpolation is not allowed"},
		"interp HOME label":            {map[string]string{"modules/m1/lab/compose.yaml": "services:\n  box:\n    image: alpine:3.22\n    labels: {a: $HOME}\n"}, "interpolation is not allowed"},
		"interp default image":         {map[string]string{"modules/m1/lab/compose.yaml": "services:\n  box:\n    image: evil.example${DOCKER_CONFIG:-y}:x\n"}, "interpolation is not allowed"},
		"interp default env map":       {map[string]string{"modules/m1/lab/compose.yaml": "services:\n  box:\n    image: alpine:3.22\n    environment: {X: \"${X:-y}\"}\n"}, "interpolation is not allowed"},
		"interp in key":                {map[string]string{"modules/m1/lab/compose.yaml": "services:\n  box:\n    image: alpine:3.22\n    labels: {\"$K\": v}\n"}, "interpolation is not allowed"},
		"interp odd dollars":           {map[string]string{"modules/m1/lab/compose.yaml": "services:\n  box:\n    image: alpine:3.22\n    command: \"echo $$$HOME\"\n"}, "interpolation is not allowed"},
		"bad regex":                    {map[string]string{"modules/m1/quiz.yaml": "questions:\n  - {id: q1, type: regex, prompt: P, answer: '('}\n  - {id: q2, type: terminal, prompt: X, check: checks/q2.sh}\n"}, "regex"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			tr, probs := Load(tree(t, c.override))
			if tr != nil {
				t.Fatal("expected nil training when problems exist")
			}
			var all []string
			for _, p := range probs {
				all = append(all, p.String())
			}
			if !strings.Contains(strings.Join(all, "\n"), c.want) {
				t.Fatalf("want problem containing %q, got:\n%s", c.want, strings.Join(all, "\n"))
			}
		})
	}
}

func TestLoadRejectsSymlinks(t *testing.T) {
	dir := tree(t, nil)
	if err := os.Symlink("/etc/passwd", filepath.Join(dir, "modules/m1/reading/intro.md")+".lnk"); err != nil {
		t.Fatal(err)
	}
	tr, probs := Load(dir)
	if tr != nil || len(probs) == 0 || !strings.Contains(probs[0].String(), "symlinks are not allowed") {
		t.Fatalf("symlink must be rejected, got %v", probs)
	}
}

func TestLocalComposeAllowsNamedVolumesAndRelativeBinds(t *testing.T) {
	compose := `x-common: &common {image: "alpine:3.22"}
services:
  box:
    <<: *common
    cap_drop: [ALL]
    env_file: [lab.env, {path: more.env, required: false}]
    networks: [inner]
    volumes: ["data:/data", "./files:/files:ro", /scratch, {type: volume, source: data, target: /d2}, {type: tmpfs, target: /t}, {type: bind, source: ./ro, target: /r, read_only: true}, "./ro2:/r2:ro,z"]
volumes: {data: {}, logs: {labels: {a: b}}}
networks: {inner: {internal: true}, plain: {driver: bridge}}
`
	_, probs := Load(tree(t, map[string]string{"modules/m1/lab/compose.yaml": compose}))
	if len(probs) > 0 {
		t.Fatalf("unexpected problems: %v", probs)
	}
}

func TestLocalComposeAllowsEscapedDollarAndTrailingEmptyDocument(t *testing.T) {
	compose := "services:\n  box:\n    image: alpine:3.22\n    command: echo $$HOME\n---\n"
	_, probs := Load(tree(t, map[string]string{"modules/m1/lab/compose.yaml": compose}))
	if len(probs) > 0 {
		t.Fatalf("unexpected problems: %v", probs)
	}
}

func TestClusterComposeMayUseHostAccess(t *testing.T) {
	lab := strings.Replace(base["modules/m1/lab/lab.yaml"], "runtime: local", "runtime: cluster", 1)
	_, probs := Load(tree(t, map[string]string{"modules/m1/lab/lab.yaml": lab,
		"modules/m1/lab/compose.yaml": "services:\n  box:\n    image: alpine:3.22\n    privileged: true\n"}))
	if len(probs) > 0 {
		t.Fatalf("cluster runtime is not restricted here: %v", probs)
	}
}

func TestHumanItemsLoad(t *testing.T) {
	tr, probs := Load(tree(t, map[string]string{
		"modules/m1/quiz.yaml": "questions:\n  - {id: q1, type: text, prompt: \"Why?\", rubric: Mentions X, points: 5}\n" +
			"  - {id: q3, type: signoff, prompt: Demo}\n  - {id: q2, type: terminal, prompt: X, check: checks/q2.sh}\n",
		"modules/m1/lab/lab.yaml": strings.Replace(base["modules/m1/lab/lab.yaml"], "    check: {script: checks/t1.sh, run_in: box}\n",
			"    human_review: true\n    rubric: The transcript shows it\n", 1),
	}))
	if len(probs) > 0 {
		t.Fatalf("unexpected problems: %v", probs)
	}
	if got := tr.Modules[0].Lab.Task("t1").Rubric; got != "The transcript shows it" {
		t.Fatalf("task rubric = %q", got)
	}
}

func TestForge301Loads(t *testing.T) {
	tr, probs := Load("../../examples/forge-301")
	if len(probs) > 0 {
		t.Fatalf("forge-301: %v", probs)
	}
	q := tr.Module("01-temper").Quiz
	if q.Question("q-why").Type != "text" || q.Question("q-log").Type != "upload" || q.Question("q-demo").Type != "signoff" {
		t.Fatal("forge-301 must have text, upload and signoff questions")
	}
	if lab := tr.Module("02-review-lab").Lab; !lab.Task("t2-proof").HumanReview || lab.Task("t1-light").Check == nil {
		t.Fatal("forge-301's lab needs one checked task and one review task")
	}
}
