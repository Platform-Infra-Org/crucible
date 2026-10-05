package notify

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"

	"github.com/riverqueue/river"

	"crucible/internal/auth"
	"crucible/internal/config"
	"crucible/internal/db/dbtest"
	"crucible/internal/gitsync"
	"crucible/internal/jobs"
)

// fakeSMTP accepts one plain SMTP session and hands back the message body it received.
func fakeSMTP(t *testing.T) (string, <-chan string) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	got := make(chan string, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		tp := textproto.NewConn(c)
		_ = tp.PrintfLine("220 fake ESMTP")
		for {
			line, err := tp.ReadLine()
			if err != nil {
				return
			}
			switch strings.ToUpper(strings.Fields(line + " x")[0]) {
			case "EHLO", "HELO":
				_ = tp.PrintfLine("250 fake")
			case "DATA":
				_ = tp.PrintfLine("354 go ahead")
				b, _ := tp.ReadDotBytes()
				got <- string(b)
				_ = tp.PrintfLine("250 ok")
			case "QUIT":
				_ = tp.PrintfLine("221 bye")
				return
			default: // MAIL, RCPT, RSET, NOOP
				_ = tp.PrintfLine("250 ok")
			}
		}
	}()
	return ln.Addr().String(), got
}

func TestEmailIsSentWithoutHeaderInjection(t *testing.T) {
	addr, got := fakeSMTP(t)
	s := &Service{SMTP: SMTPConfig{Addr: addr, From: "crucible@example.com"}}
	err := s.sendEmail(EmailArgs{To: "trainee@crucible.local", Subject: "Lab \"x\"\r\nBcc: evil@example.com", Body: "Hello.\n.\nhttps://crucible.example/approvals"})
	if err != nil {
		t.Fatal(err)
	}
	msg := <-got
	if strings.Contains(msg, "\nBcc:") {
		t.Fatalf("header injected:\n%s", msg)
	}
	for _, want := range []string{"To: trainee@crucible.local", "Subject: ", "Content-Type: text/plain; charset=UTF-8", "https://crucible.example/approvals"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("missing %q in:\n%s", want, msg)
		}
	}
}

func TestWebhooksPostSlackTextAndTeamsCards(t *testing.T) {
	var bodies []map[string]any
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m map[string]any
		_ = json.NewDecoder(r.Body).Decode(&m)
		bodies = append(bodies, m)
		if r.URL.Path == "/broken" {
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer srv.Close()
	plat, err := config.Load("../../examples/platform")
	if err != nil {
		t.Fatal(err)
	}
	plat.Teams["forge"].Notifications = config.TeamNotifications{SlackWebhook: srv.URL + "/slack", TeamsWebhook: srv.URL + "/broken"}
	st := &gitsync.State{Platform: plat}
	s := &Service{State: func() *gitsync.State { return st }, HTTP: srv.Client()}
	if err := s.postWebhook(context.Background(), WebhookArgs{Team: "forge", Flavor: "slack", Text: "hi"}); err != nil {
		t.Fatal(err)
	}
	if bodies[0]["text"] != "hi" {
		t.Fatalf("slack payload %v", bodies[0])
	}
	if err := s.postWebhook(context.Background(), WebhookArgs{Team: "forge", Flavor: "teams", Text: "hi"}); err == nil {
		t.Fatal("a non-2xx answer must fail the job so River retries it")
	}
	if bodies[1]["type"] != "message" || bodies[1]["attachments"] == nil {
		t.Fatalf("teams payload %v", bodies[1])
	}
	if err := s.postWebhook(context.Background(), WebhookArgs{Team: "gone", Flavor: "slack", Text: "hi"}); err != nil {
		t.Fatalf("a team removed since queueing is skipped, got %v", err)
	}
}

func TestNotifyQueuesUnmutedEmailsAndTeamPosts(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)
	plat, _ := config.Load("../../examples/platform")
	plat.Teams["forge"].Notifications.SlackWebhook = "https://hooks.example/slack"
	st := &gitsync.State{Platform: plat}
	s := &Service{DB: pool, State: func() *gitsync.State { return st }, PublicURL: "https://crucible.example",
		SMTP: SMTPConfig{Addr: "127.0.0.1:1", From: "c@x"}, Log: slog.Default()}
	workers := river.NewWorkers()
	river.AddWorker(workers, &EmailWorker{S: s})
	river.AddWorker(workers, &WebhookWorker{S: s})
	client, err := jobs.New(pool, workers, nil, slog.Default()) // never started: insert only
	if err != nil {
		t.Fatal(err)
	}
	s.Jobs = client
	trainee, _ := auth.Store{DB: pool}.UpsertUser(ctx, "s1", "trainee@crucible.local", "Tara")
	if err := s.SetMutes(ctx, trainee.ID, []Kind{LabApproved}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetMutes(ctx, trainee.ID, []Kind{"bogus"}); err == nil {
		t.Fatal("unknown kinds must be rejected")
	}
	err = s.Notify(ctx, Event{Kind: LabApproved, To: []string{"Trainee@crucible.local", "leader@crucible.local", "leader@crucible.local"},
		Team: "forge", Subject: "Approved", Text: "Go.", Link: "/p/forge/forge-101/m/02-first-lab/lab"})
	if err != nil {
		t.Fatal(err)
	}
	rows, _ := pool.Query(ctx, `SELECT kind, args FROM river_job ORDER BY id`)
	defer rows.Close()
	var got []string
	for rows.Next() {
		var kind string
		var args map[string]any
		_ = rows.Scan(&kind, &args)
		got = append(got, kind+":"+anyStr(args["to"])+anyStr(args["flavor"]))
		if kind == "notify_email" && !strings.Contains(anyStr(args["body"]), "https://crucible.example/p/forge/forge-101/m/02-first-lab/lab") {
			t.Fatalf("email body lacks the link: %v", args)
		}
	}
	if strings.Join(got, ",") != "notify_email:leader@crucible.local,notify_webhook:slack" {
		t.Fatalf("queued %v (the muted trainee gets no email, duplicates collapse)", got)
	}
}

func anyStr(v any) string { s, _ := v.(string); return s }

func TestWebhookSSRFGuard(t *testing.T) {
	plat, _ := config.Load("../../examples/platform")
	st := &gitsync.State{Platform: plat}
	for _, u := range []string{"https://127.0.0.1/x", "https://169.254.169.254/latest", "https://10.0.0.1/x", "https://[::1]/x", "http://hooks.example/x"} {
		plat.Teams["forge"].Notifications = config.TeamNotifications{SlackWebhook: u}
		s := &Service{State: func() *gitsync.State { return st }} // default guarded client
		if err := s.postWebhook(context.Background(), WebhookArgs{Team: "forge", Flavor: "slack", Text: "x"}); err == nil {
			t.Fatalf("%s must be refused", u)
		}
	}
}

func TestGuardedClientAllowsLoopbackOnlyWhenTold(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	if _, err := guardedClient(false).Get(srv.URL); err == nil {
		t.Fatal("loopback must be refused")
	}
	c := guardedClient(true)
	c.Transport.(*http.Transport).TLSClientConfig = srv.Client().Transport.(*http.Transport).TLSClientConfig
	if _, err := c.Get(srv.URL); err != nil {
		t.Fatal(err)
	}
}
