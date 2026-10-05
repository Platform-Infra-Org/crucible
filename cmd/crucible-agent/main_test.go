package main

import "testing"

func TestCheckServer(t *testing.T) {
	for s, ok := range map[string]bool{
		"https://crucible.example.com": true, "http://localhost:8080": true, "http://127.0.0.1:8080": true,
		"http://[::1]:8080": true, "http://crucible.example.com": false, "http://10.0.0.5:8080": false,
		"ftp://localhost": false, "localhost:8080": false, "http://localhost.evil.com": false,
	} {
		if got := checkServer(s) == nil; got != ok {
			t.Errorf("%s: allowed=%v, want %v", s, got, ok)
		}
	}
}
