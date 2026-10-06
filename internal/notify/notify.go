// Package notify delivers Crucible events by email (SMTP) and Slack/Teams incoming webhooks (spec §10).
// Each delivery is a River job, so a flaky mail server or webhook is retried instead of lost.
package notify

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/smtp"
	"net/textproto"
	neturl "net/url"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"crucible/internal/apperr"
	"crucible/internal/content"
	"crucible/internal/gitsync"
)

type Kind string

const (
	LabPending        Kind = "lab_request_pending"
	LabEscalated      Kind = "lab_request_escalated"
	LabApproved       Kind = "lab_request_approved"
	LabRejected       Kind = "lab_request_rejected" // also expired
	BudgetAlert       Kind = "budget_alert"
	SyncFailed        Kind = "content_sync_failed"
	SetupFailed       Kind = "setup_failed"
	SubmissionPending Kind = "submission_pending"
	SubmissionScored  Kind = "submission_scored" // also returned for rework
	ReaperReport      Kind = "reaper_report"
	RankUp            Kind = "rank_up"
)

type KindInfo struct {
	Kind  Kind   `json:"kind"`
	Label string `json:"label"`
}

// Kinds lists every event a user can mute by email, with the label the settings page shows.
var Kinds = []KindInfo{
	{LabPending, "A lab request is waiting for my approval"},
	{LabEscalated, "A lab request was escalated to me"},
	{LabApproved, "My lab request was approved"},
	{LabRejected, "My lab request was rejected or expired"},
	{BudgetAlert, "A budget reaches 80% or its hard cap"},
	{SyncFailed, "Content I maintain failed to sync"},
	{SetupFailed, "A lab scenario I maintain failed to prepare"},
	{SubmissionPending, "A submission is waiting for my score"},
	{SubmissionScored, "My submission was scored or returned"},
	{ReaperReport, "Leftover AWS lab resources need attention"},
	{RankUp, "I or one of my mentees reached a new forge rank"},
}

type Event struct {
	Kind    Kind
	To      []string // recipient emails
	Team    string   // when set, also posted to the team's Slack/Teams webhooks
	Subject string
	Text    string
	Link    string // app path, e.g. /approvals
}

type SMTPConfig struct{ Addr, From, Username, Password string } // Addr "" turns email off

type Service struct {
	DB        *pgxpool.Pool
	Jobs      *river.Client[pgx.Tx]
	State     func() *gitsync.State
	PublicURL string
	SMTP      SMTPConfig
	HTTP      *http.Client // webhooks; nil = guarded client (10 s timeout, no redirects, no private/loopback dials)
	// AllowLoopback lets the default client dial loopback; tests only.
	AllowLoopback bool

	once    sync.Once
	guarded *http.Client
	Log     *slog.Logger
}

// blockedIP reports addresses a webhook must never reach (SSRF): loopback, private, link-local, unspecified, multicast.
func blockedIP(ip net.IP, allowLoopback bool) bool {
	a, ok := netip.AddrFromSlice(ip)
	if !ok {
		return true
	}
	a = a.Unmap()
	if allowLoopback && a.IsLoopback() {
		return false
	}
	return !a.IsGlobalUnicast() || a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() || a.IsMulticast() || a.IsUnspecified() ||
		slices.ContainsFunc(deniedPrefixes, func(p netip.Prefix) bool { return p.Contains(a) })
}

// deniedPrefixes are non-public ranges the netip predicates miss (several embed IPv4 and so can reach private hosts).
var deniedPrefixes = func() (out []netip.Prefix) {
	for _, p := range []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "198.18.0.0/15", "240.0.0.0/4", "64:ff9b::/96", "2002::/16"} {
		out = append(out, netip.MustParsePrefix(p))
	}
	return
}()

// guardedClient checks the resolved IP at dial time (Control runs after DNS), so DNS rebinding cannot bypass it.
func guardedClient(allowLoopback bool) *http.Client {
	d := &net.Dialer{Timeout: 10 * time.Second, Control: func(_, address string, _ syscall.RawConn) error {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return err
		}
		if blockedIP(net.ParseIP(host), allowLoopback) {
			return fmt.Errorf("webhook target %s is not a public address", host)
		}
		return nil
	}}
	return &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{DialContext: d.DialContext}, // no proxy
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }} // no redirect hops to internal hosts
}

func (s *Service) client() *http.Client {
	if s.HTTP != nil {
		return s.HTTP
	}
	s.once.Do(func() { s.guarded = guardedClient(s.AllowLoopback) })
	return s.guarded
}

const maxAttempts = 8

// Notify queues one email per unmuted recipient and one post per configured team webhook.
func (s *Service) Notify(ctx context.Context, ev Event) error {
	body := ev.Text
	if ev.Link != "" {
		body += "\n\n" + s.PublicURL + ev.Link
	}
	opts := &river.InsertOpts{MaxAttempts: maxAttempts}
	if s.SMTP.Addr != "" {
		to, err := s.unmuted(ctx, ev.Kind, ev.To)
		if err != nil {
			return err
		}
		for _, addr := range to {
			if _, err := s.Jobs.Insert(ctx, EmailArgs{To: addr, Subject: ev.Subject, Body: body}, opts); err != nil {
				return err
			}
		}
	}
	if ev.Team != "" {
		for _, flavor := range []string{"slack", "teams"} {
			if s.webhookURL(ev.Team, flavor) == "" {
				continue
			}
			if _, err := s.Jobs.Insert(ctx, WebhookArgs{Team: ev.Team, Flavor: flavor, Text: ev.Subject + "\n" + body}, opts); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Service) unmuted(ctx context.Context, kind Kind, to []string) ([]string, error) {
	var list []string
	for _, e := range to {
		if e = strings.ToLower(strings.TrimSpace(e)); e != "" && !slices.Contains(list, e) {
			list = append(list, e)
		}
	}
	rows, err := s.DB.Query(ctx, `SELECT u.email FROM notification_mutes m JOIN users u ON u.id = m.user_id
		WHERE m.kind = $1 AND u.email = ANY($2)`, string(kind), list)
	if err != nil {
		return nil, err
	}
	muted, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(list, func(e string) bool { return slices.Contains(muted, e) }), nil
}

func (s *Service) webhookURL(team, flavor string) string {
	st := s.State()
	if st == nil || st.Platform == nil || st.Platform.Teams[team] == nil {
		return ""
	}
	n := st.Platform.Teams[team].Notifications
	if flavor == "teams" {
		return n.TeamsWebhook
	}
	return n.SlackWebhook
}

type EmailArgs struct {
	To      string `json:"to"`
	Subject string `json:"subject"`
	Body    string `json:"body"`
}

func (EmailArgs) Kind() string { return "notify_email" }

type EmailWorker struct {
	river.WorkerDefaults[EmailArgs]
	S *Service
}

func (w *EmailWorker) Work(_ context.Context, j *river.Job[EmailArgs]) error {
	return w.S.sendEmail(j.Args)
}

var headerSafe = strings.NewReplacer("\r", " ", "\n", " ")

func (s *Service) sendEmail(a EmailArgs) error {
	if s.SMTP.Addr == "" {
		return nil
	}
	id := make([]byte, 12)
	_, _ = rand.Read(id)
	domain := "localhost"
	if _, d, ok := strings.Cut(s.SMTP.From, "@"); ok {
		domain = headerSafe.Replace(strings.Trim(d, "<> "))
	}
	body := strings.ReplaceAll(strings.ReplaceAll(a.Body, "\r\n", "\n"), "\n", "\r\n")
	msg := "From: " + headerSafe.Replace(s.SMTP.From) + "\r\nDate: " + time.Now().Format(time.RFC1123Z) +
		"\r\nMessage-ID: <" + hex.EncodeToString(id) + "@" + domain + ">\r\nTo: " + headerSafe.Replace(a.To) +
		"\r\nSubject: " + mime.QEncoding.Encode("utf-8", headerSafe.Replace(a.Subject)) +
		"\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n" +
		body
	var auth smtp.Auth
	if s.SMTP.Username != "" {
		host, _, _ := net.SplitHostPort(s.SMTP.Addr)
		auth = smtp.PlainAuth("", s.SMTP.Username, s.SMTP.Password, host) // net/smtp refuses PLAIN without TLS except on localhost
	}
	err := smtp.SendMail(s.SMTP.Addr, auth, s.SMTP.From, []string{a.To}, []byte(msg))
	var te *textproto.Error
	if errors.As(err, &te) && te.Code >= 500 {
		return river.JobCancel(err) // permanent rejection (bad recipient, auth): retrying only repeats it
	}
	return err
}

type WebhookArgs struct {
	Team   string `json:"team"`
	Flavor string `json:"flavor"` // slack | teams
	Text   string `json:"text"`
}

func (WebhookArgs) Kind() string { return "notify_webhook" }

type WebhookWorker struct {
	river.WorkerDefaults[WebhookArgs]
	S *Service
}

func (w *WebhookWorker) Work(ctx context.Context, j *river.Job[WebhookArgs]) error {
	return w.S.postWebhook(ctx, j.Args)
}

// postWebhook resolves the URL at send time (it lives in team.yaml, not in the job row).
func (s *Service) postWebhook(ctx context.Context, a WebhookArgs) error {
	url := s.webhookURL(a.Team, a.Flavor)
	if url == "" {
		return nil // removed from team.yaml since it was queued
	}
	if a.Flavor == "slack" {
		a.Text = slackEscape.Replace(a.Text) // mrkdwn control sequences: <!channel>, <url|label>
	}
	if u, err := neturl.Parse(url); err != nil || u.Scheme != "https" || u.Hostname() == "" {
		return river.JobCancel(fmt.Errorf("%s webhook for team %s must be an https URL", a.Flavor, a.Team)) // retrying cannot fix config
	}
	var payload any = map[string]string{"text": a.Text}
	if a.Flavor == "teams" {
		payload = teamsCard(a.Text)
	}
	b, _ := json.Marshal(payload)
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client().Do(req)
	if err != nil {
		var ue *neturl.Error
		if errors.As(err, &ue) {
			err = ue.Err // never leak the secret webhook URL into logs or job errors
		}
		return fmt.Errorf("%s webhook for team %s: %w", a.Flavor, a.Team, err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode >= 300 {
		err := fmt.Errorf("%s webhook for team %s: HTTP %d", a.Flavor, a.Team, resp.StatusCode)
		c := resp.StatusCode
		if c < 400 || (c < 500 && c != http.StatusRequestTimeout && c != http.StatusTooManyRequests) {
			return river.JobCancel(err) // redirects and permanent client errors will not heal on retry
		}
		return err
	}
	return nil
}

var slackEscape = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

// teamsCard wraps text in the Adaptive Card envelope that Teams "Workflows" incoming webhooks accept.
func teamsCard(text string) any {
	return map[string]any{"type": "message", "attachments": []any{map[string]any{
		"contentType": "application/vnd.microsoft.card.adaptive",
		"content": map[string]any{"$schema": "http://adaptivecards.io/schemas/adaptive-card.json", "type": "AdaptiveCard", "version": "1.4",
			"body": []any{map[string]any{"type": "TextBlock", "text": text, "wrap": true}}},
	}}}
}

// Mutes lists the kinds a user muted.
func (s *Service) Mutes(ctx context.Context, userID int64) ([]Kind, error) {
	rows, err := s.DB.Query(ctx, `SELECT kind FROM notification_mutes WHERE user_id = $1`, userID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[Kind])
}

// SetMutes replaces the user's muted kinds.
func (s *Service) SetMutes(ctx context.Context, userID int64, kinds []Kind) error {
	for _, k := range kinds {
		if !slices.ContainsFunc(Kinds, func(i KindInfo) bool { return i.Kind == k }) {
			return apperr.Wrap(apperr.Invalid, fmt.Sprintf("unknown notification kind %q", k))
		}
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `DELETE FROM notification_mutes WHERE user_id = $1`, userID); err != nil {
		return err
	}
	for _, k := range kinds {
		if _, err := tx.Exec(ctx, `INSERT INTO notification_mutes (user_id, kind) VALUES ($1, $2) ON CONFLICT DO NOTHING`, userID, string(k)); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// ReportSyncProblem tells admins, and the maintainers of the affected training, that a new commit was rejected
// (spec §6, §10). key is "platform", "<training>", "<training>@<sha>" or "<team>/<training>" (a bad pin).
func (s *Service) ReportSyncProblem(ctx context.Context, key string, probs []content.Problem) {
	st := s.State()
	if st == nil || st.Platform == nil {
		return
	}
	to := slices.Clone(st.Platform.Admins)
	what := "The platform config"
	if key != "platform" {
		training, _, _ := strings.Cut(key, "@")
		if _, tr, ok := strings.Cut(training, "/"); ok {
			training = tr
		}
		what = "Training " + training
		for _, t := range st.Trainings {
			if t.ID == training {
				to = append(to, t.Maintainers...)
			}
		}
	}
	var lines []string
	for i, p := range probs {
		if i == 5 {
			lines = append(lines, fmt.Sprintf("…and %d more", len(probs)-5))
			break
		}
		lines = append(lines, p.String())
	}
	err := s.Notify(ctx, Event{Kind: SyncFailed, To: to, Subject: what + " failed to sync",
		Text: what + " has a new commit that Crucible rejected; the previous version stays live.\n\n" + strings.Join(lines, "\n"),
		Link: "/admin"})
	if err != nil {
		s.Log.Error("queueing sync-failure notification failed", "key", key, "err", err)
	}
}
