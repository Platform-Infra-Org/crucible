package main

import (
	"log/slog"
	"net/http"
	"os"

	"crucible/internal/httpapi"
)

func main() {
	addr := env("CRUCIBLE_ADDR", ":8080")
	slog.Info("crucible-api listening", "addr", addr)
	if err := http.ListenAndServe(addr, httpapi.NewRouter(httpapi.Deps{})); err != nil {
		slog.Error("server stopped", "err", err)
		os.Exit(1)
	}
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
