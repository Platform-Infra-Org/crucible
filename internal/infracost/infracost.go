// Package infracost prices an aws lab's terraform module with the infracost CLI (spec §9.1: "aws = infracost on the
// module × requested TTL"). Prices come from Infracost's pricing API, never from AWS. It runs inside crucible-api on
// author-supplied terraform, so: lint keeps module sources local (./ paths) and --no-cache stops it reusing or
// saving downloaded modules; it runs on a throwaway copy with a throwaway HOME; its environment is an allowlist
// (no AWS credentials, no cloud tokens); and it has a timeout and an output cap.
package infracost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"crucible/internal/content"
)

const (
	timeout   = 2 * time.Minute
	maxOutput = 8 << 20
)

// Runner runs the infracost CLI in dir and returns its stdout.
type Runner func(ctx context.Context, dir string, env []string, args ...string) ([]byte, error)

// passEnv is all Exec inherits from the server's environment: where to find the binary and certificates, proxies,
// and Infracost's own settings. Everything else (AWS_*, tokens, DATABASE_URL, ...) is dropped.
var passEnv = []string{"PATH", "SSL_CERT_FILE", "SSL_CERT_DIR", "HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY", "https_proxy",
	"http_proxy", "no_proxy", "INFRACOST_API_KEY", "INFRACOST_PRICING_API_ENDPOINT"}

// limitWriter fails once more than max bytes were written.
type limitWriter struct {
	buf bytes.Buffer
	max int
}

func (w *limitWriter) Write(p []byte) (int, error) {
	if w.buf.Len()+len(p) > w.max {
		return 0, errors.New("output too large")
	}
	return w.buf.Write(p)
}

// Exec is the real CLI. env (e.g. HOME) is added to the allowlisted environment.
func Exec(ctx context.Context, dir string, env []string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "infracost", args...)
	cmd.Dir, cmd.WaitDelay = dir, 5*time.Second
	cmd.Env = []string{}
	for _, k := range passEnv {
		if v, ok := os.LookupEnv(k); ok {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}
	cmd.Env = append(cmd.Env, env...)
	out, stderr := &limitWriter{max: maxOutput}, &limitWriter{max: 64 << 10}
	cmd.Stdout, cmd.Stderr = out, stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.buf.String())
		if len(msg) > 500 {
			msg = msg[len(msg)-500:]
		}
		return nil, fmt.Errorf("infracost: %w: %s", err, msg)
	}
	return out.buf.Bytes(), nil
}

// Hourly prices the module in moduleDir as it would run in region, with Crucible's provider file added (on a temp
// copy: content exports are shared). Usage-based resources (S3 requests, data transfer) count as $0: the result is
// a floor, which authors bound with aws.max_hourly_usd.
func Hourly(ctx context.Context, run Runner, moduleDir, region string) (float64, error) {
	tmp, err := os.MkdirTemp("", "crucible-infracost-*")
	if err != nil {
		return 0, err
	}
	defer os.RemoveAll(tmp)
	home, mod := filepath.Join(tmp, "home"), filepath.Join(tmp, "module") // HOME stays outside the scanned path
	if err := os.Mkdir(home, 0o700); err != nil {
		return 0, err
	}
	if err := os.CopyFS(mod, os.DirFS(moduleDir)); err != nil { // refuses symlinks
		return 0, err
	}
	files := map[string][]byte{content.LabTFFile: []byte(content.LabTF),
		content.LabTFVarsFile: content.LabTFVars("estimate", "estimate", "estimate", region)}
	for name, b := range files {
		if err := os.WriteFile(filepath.Join(mod, name), b, 0o644); err != nil {
			return 0, err
		}
	}
	out, err := run(ctx, mod, []string{"HOME=" + home, "INFRACOST_SKIP_UPDATE_CHECK=true", "INFRACOST_NO_COLOR=true"},
		"breakdown", "--path", ".", "--format", "json", "--log-level", "warn", "--no-cache")
	if err != nil {
		return 0, err
	}
	var r struct {
		TotalHourlyCost *string `json:"totalHourlyCost"`
	}
	if err := json.Unmarshal(out, &r); err != nil {
		return 0, fmt.Errorf("reading infracost output: %w", err)
	}
	if r.TotalHourlyCost == nil {
		return 0, nil
	}
	h, err := strconv.ParseFloat(*r.TotalHourlyCost, 64)
	if err != nil || h < 0 || math.IsNaN(h) || math.IsInf(h, 0) {
		return 0, fmt.Errorf("infracost price %q is not a price", *r.TotalHourlyCost)
	}
	return h, nil
}
