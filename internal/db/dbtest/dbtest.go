// Package dbtest gives each test its own migrated Postgres database inside one shared container.
package dbtest

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"crucible/internal/db"
)

var (
	once     sync.Once
	baseURL  string
	startErr error
	counter  atomic.Int64
)

func New(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()
	once.Do(func() {
		// Under enforcing SELinux the ryuk reaper cannot bind-mount /var/run/docker.sock and never starts,
		// so every run stalls for a minute. Skip it there, unless the developer chose a value.
		if b, _ := os.ReadFile("/sys/fs/selinux/enforce"); strings.TrimSpace(string(b)) == "1" {
			if _, set := os.LookupEnv("TESTCONTAINERS_RYUK_DISABLED"); !set {
				os.Setenv("TESTCONTAINERS_RYUK_DISABLED", "true")
			}
		}
		c, err := postgres.Run(ctx, "postgres:18-alpine",
			postgres.WithDatabase("crucible"), postgres.WithUsername("crucible"), postgres.WithPassword("crucible"),
			postgres.BasicWaitStrategies())
		if err != nil {
			startErr = err
			return
		}
		baseURL, startErr = c.ConnectionString(ctx, "sslmode=disable")
	})
	if startErr != nil {
		t.Fatalf("start postgres (is Docker running?): %v", startErr)
	}
	name := fmt.Sprintf("t_%d_%d", counter.Add(1), os.Getpid())
	admin, err := pgx.Connect(ctx, baseURL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	_ = admin.Close(ctx)
	u, _ := url.Parse(baseURL)
	u.Path = "/" + name
	pool, err := db.Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}
