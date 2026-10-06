package content

import (
	"encoding/json"
	"maps"
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

// awsTree is base with its lab turned into a minimal valid aws lab; override replaces or adds files on top.
func awsTree(t *testing.T, override map[string]string) string {
	t.Helper()
	files := map[string]string{
		"modules/m1/lab/lab.yaml": `id: l1
runtime: aws
terminals: [{name: ws, service: workspace}]
tasks:
  - id: t1
    instructions: tasks/t1.md
    check: {script: checks/t1.sh, run_in: workspace}
aws: {region: eu-west-1, max_hourly_usd: 0.1}
`,
		"modules/m1/lab/compose.yaml":      "<delete>",
		"modules/m1/lab/setup/t2.sh":       "<delete>",
		"modules/m1/lab/hints/sol.md":      "<delete>",
		"modules/m1/lab/terraform/main.tf": "resource \"aws_s3_bucket\" \"b\" {\n  bucket = \"crucible-lab-${var.crucible_lab_id}\"\n}\n",
	}
	maps.Copy(files, override)
	return tree(t, files)
}

func TestAWSLabModuleRules(t *testing.T) {
	if _, probs := Load(awsTree(t, nil)); len(probs) > 0 {
		t.Fatalf("a minimal aws lab must load: %v", probs)
	}
	withAWS := func(aws string) map[string]string {
		lab := strings.Replace(`id: l1
runtime: aws
terminals: [{name: ws, service: workspace}]
tasks:
  - id: t1
    instructions: tasks/t1.md
    check: {script: checks/t1.sh, run_in: workspace}
AWS`, "AWS", aws, 1)
		return map[string]string{"modules/m1/lab/lab.yaml": lab}
	}
	tf := func(name, body string) map[string]string {
		return map[string]string{"modules/m1/lab/terraform/" + name: body}
	}
	cases := map[string]struct {
		override map[string]string
		want     string
	}{
		"no region":    {withAWS("aws: {max_hourly_usd: 0.1}\n"), "aws.region is required"},
		"odd region":   {withAWS("aws: {region: \"eu-west-1; rm -rf\", max_hourly_usd: 0.1}\n"), "aws.region is required"},
		"no ceiling":   {withAWS("aws: {region: eu-west-1}\n"), "aws.max_hourly_usd must be set"},
		"no module":    {tf("main.tf", "<delete>"), "needs a terraform/ directory"},
		"own provider": {tf("p.tf", "provider \"aws\" {\n  region = \"us-east-1\"\n}\n"), "do not declare provider blocks"},
		"own backend":  {tf("b.tf", "terraform {\n  backend \"local\" {}\n}\n"), "do not declare a backend"},
		"registry mod": {tf("m.tf", "module \"vpc\" {\n  source = \"terraform-aws-modules/vpc/aws\"\n}\n"), "must be a local path"},
		"git module":   {tf("m.tf", "module \"x\" {\n  source = \"git::https://example.com/x.git\"\n}\n"), "must be a local path"},
		// review bypasses (task-1-review.md): each passed the regex lint
		"one-line git module":    {tf("m.tf", `module "x" { source = "git::https://evil/x.git" }`), "must be a local path"},
		"one-line backend":       {tf("x_override.tf", `terraform { backend "local" {} }`), "terraform/x_override.tf"}, // invalid HCL: a parse error
		"override backend":       {tf("x_override.tf", "terraform {\nbackend \"local\" {}\n}\n"), "do not declare a backend"},
		"cloud block":            {tf("c.tf", "terraform {\n  cloud {}\n}\n"), "do not declare a backend"},
		"json provider":          {tf("p.tf.json", `{"provider": {"aws": {"region": "us-east-1"}}}`), "do not declare provider blocks"},
		"json backend":           {tf("b_override.tf.json", `{"terraform": {"backend": {"local": {}}}}`), "do not declare a backend"},
		"json module":            {tf("m.tf.json", `{"module": {"x": {"source": "git::https://evil/x.git"}}}`), "must be a local path"},
		"github shorthand":       {tf("m.tf", `module "x" { source = "github.com/evil/repo" }`), "must be a local path"},
		"bitbucket shorthand":    {tf("m.tf", `module "x" { source = "bitbucket.org/x/y" }`), "must be a local path"},
		"https module":           {tf("m.tf", `module "x" { source = "https://evil/x.zip" }`), "must be a local path"},
		"s3 module":              {tf("m.tf", `module "x" { source = "s3::https://s3.amazonaws.com/b/x.zip" }`), "must be a local path"},
		"escaping module":        {tf("m.tf", `module "x" { source = "./../../.." }`), "must be a local path"},
		"evil provider source":   {tf("v.tf", "terraform {\n  required_providers {\n    aws = { source = \"evil.example/x/aws\" }\n  }\n}\n"), "provider source"},
		"json provider source":   {tf("v.tf.json", `{"terraform": {"required_providers": {"aws": {"source": "evil/aws"}}}}`), "provider source"},
		"auto tfvars":            {tf("zz.auto.tfvars", "crucible_team = \"other\"\n"), "do not ship"},
		"tfvars json":            {tf("x.tfvars.json", `{}`), "do not ship"},
		"plain tfvars":           {tf("terraform.tfvars", ""), "do not ship"},
		"unquoted provider":      {tf("p.tf", "provider aws {\n}\n"), "do not declare provider blocks"},
		"provider after comment": {tf("p.tf", "/* x */ provider \"aws\" {}\n"), "do not declare provider blocks"},
		"split provider":         {tf("p.tf", "provider\n\"aws\"\n{\n}\n"), "terraform/p.tf"}, // invalid HCL: a parse error
		"aliased other provider": {tf("p.tf", "provider \"random\" {\n  alias = \"x\"\n}\n"), "do not declare provider blocks"},
		"upper-case extension":   {tf("P.TF", `provider "aws" {}`), "do not declare provider blocks"},
		"nested module provider": {tf("x/main.tf", `provider "aws" {}`), "do not declare provider blocks"},
		"parse error":            {tf("bad.tf", "resource \"x\" {\n"), "terraform/bad.tf"},
		"json parse error":       {tf("bad.tf.json", "{"), "terraform/bad.tf.json"},
		"shipped .terraform dir": {tf(".terraform/providers/x", "bin"), "do not ship"},
		"too big":                {tf("big.tf", "# "+strings.Repeat("x", 600<<10)+"\n"), "keep it under 512 KiB"},
		// fix round 2 (task-1-review): shipped state, import blocks, non-literal sources, per-file cap
		"shipped tfstate":        {tf("terraform.tfstate", "{}"), "do not ship"},
		"shipped tfstate backup": {tf("X.TFSTATE.BACKUP", "{}"), "do not ship"},
		"import block":           {tf("i.tf", "import {\n  to = aws_s3_bucket.b\n  id = \"other-lab\"\n}\n"), "do not declare import blocks"},
		"json import block":      {tf("i.tf.json", `{"import": [{"to": "aws_s3_bucket.b", "id": "x"}]}`), "do not declare import blocks"},
		"templated source":       {tf("m.tf", "module \"x\" {\n  source = \"./${var.x}\"\n}\n"), "must be a literal"},
		"json non-string source": {tf("m.tf.json", `{"module": {"x": {"source": ["./x"]}}}`), "must be a literal"},
		"computed provider":      {tf("v.tf", "terraform {\n  required_providers {\n    aws = { source = lower(\"HASHICORP/AWS\") }\n  }\n}\n"), "must be a literal"},
		"for provider":           {tf("v.tf", "terraform {\n  required_providers {\n    aws = { for k in [1] : \"source\" => \"hashicorp/aws\" }\n  }\n}\n"), "must be a literal"},
		"big file":               {tf("big.tf", "# "+strings.Repeat("x", 200<<10)+"\n"), "keep each file under 128 KiB"},
		"wrong terminal":         {map[string]string{"modules/m1/lab/lab.yaml": strings.Replace(withAWS("aws: {region: eu-west-1, max_hourly_usd: 0.1}\n")["modules/m1/lab/lab.yaml"], "service: workspace}]", "service: box}]", 1)}, `service "box"`},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, probs := Load(awsTree(t, c.override))
			for _, p := range probs {
				if strings.Contains(p.String(), c.want) {
					return
				}
			}
			t.Fatalf("want a problem containing %q, got %v", c.want, probs)
		})
	}
	// Provider sources in required_providers are not module sources; local modules are fine.
	ok := awsTree(t, map[string]string{
		"modules/m1/lab/terraform/versions.tf": "terraform {\n  required_providers {\n    aws = {\n      source = \"hashicorp/aws\"\n    }\n  }\n}\n",
		"modules/m1/lab/terraform/mod.tf":      "module \"x\" {\n  source = \"./x\"\n}\n",
		"modules/m1/lab/terraform/x/main.tf":   "# a local module\n",
		"modules/m1/lab/terraform/more.tf.json": `{"terraform": {"required_providers": {"random": {"source": "registry.terraform.io/hashicorp/random"}, "null": "~> 3.0"}},
			"module": {"y": {"source": "./x"}}}`,
	})
	if _, probs := Load(ok); len(probs) > 0 {
		t.Fatalf("provider sources and local modules are allowed: %v", probs)
	}
}

// The lint runs inside crucible-api on every git sync: an author's push must not crash it (stack overflow on deep
// nesting) or burn minutes of CPU (evaluating expressions). Each input must fail fast with a problem.
func TestAWSLabModuleRejectsHostileInputFast(t *testing.T) {
	nest := func(open, close string, n int) string { return strings.Repeat(open, n) + strings.Repeat(close, n) }
	list := "[" + strings.TrimSuffix(strings.Repeat("1,", 400), ",") + "]"
	cases := map[string]struct{ name, body, want string }{
		"deep list":         {"d.tf", "locals {\n  x = " + nest("[", "]", 50000) + "\n}\n", "nested too deeply"},
		"deep parens":       {"d.tf", "locals {\n  x = " + nest("(", ")", 50000) + "\n}\n", "nested too deeply"},
		"deep json":         {"d.tf.json", `{"locals": {"x": ` + nest("[", "]", 50000) + `}}`, "nested too deeply"},
		"strings hide nest": {"d.tf", "locals {\n  x = " + strings.Repeat(`["]]]",`, 10000) + strings.Repeat("]", 10000) + "\n}\n", "nested too deeply"},
		"huge deep list":    {"d.tf", "locals {\n  x = " + nest("[", "]", 250000) + "\n}\n", "keep"},
		"for bomb": {"m.tf", "locals {\n  l = " + list + "\n}\nmodule \"x\" {\n  source = [for a in local.l : [for b in local.l : [for c in local.l : [for d in local.l : 1]]]]\n}\n",
			"must be a literal"},
		"provider for bomb": {"v.tf", "terraform {\n  required_providers {\n    aws = [for a in " + list + " : [for b in " + list + " : [for c in " + list + " : 1]]]\n  }\n}\n",
			"must be a literal"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			dir := awsTree(t, map[string]string{"modules/m1/lab/terraform/" + c.name: c.body})
			start := time.Now()
			_, probs := Load(dir)
			if d := time.Since(start); d > time.Second {
				t.Errorf("lint took %v", d)
			}
			for _, p := range probs {
				if strings.Contains(p.String(), c.want) {
					return
				}
			}
			t.Fatalf("want a problem containing %q, got %v", c.want, probs)
		})
	}
}

func TestForge401IsAnAWSLab(t *testing.T) {
	tr, probs := Load("../../examples/forge-401")
	if len(probs) > 0 {
		t.Fatalf("forge-401: %v", probs)
	}
	lab := tr.Module("01-cloud-heat").Lab
	if lab.ID != "cloud-heat" || lab.Runtime != "aws" || lab.AWS.Region != "eu-west-1" || lab.AWS.MaxHourlyUSD != 0.05 || len(lab.Tasks) != 2 {
		t.Fatalf("cloud-heat: %+v", lab)
	}
}

func TestLabTFVarsIsJSON(t *testing.T) {
	var v map[string]string
	if err := json.Unmarshal(LabTFVars("abcdefabcdef", `te"am`, "tr", "eu-west-1"), &v); err != nil || v["crucible_team"] != `te"am` || v["crucible_region"] != "eu-west-1" {
		t.Fatalf("tfvars must be JSON so no value escapes its string: %v %v", v, err)
	}
	for _, want := range []string{`backend "s3" {}`, `provider "aws"`, `"crucible:lab-id"   = var.crucible_lab_id`} {
		if !strings.Contains(LabTF, want) {
			t.Fatalf("LabTF lacks %q", want)
		}
	}
}
