package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runMainEnv makes the test binary act as the transcoder itself, so exit
// codes and stderr are tested without a separate build step. The pipeline
// itself is tested in internal/transcode.
const runMainEnv = "TRANSCODER_TEST_RUN_MAIN"

func TestMain(m *testing.M) {
	if os.Getenv(runMainEnv) == "1" {
		os.Exit(realMain(os.Args[1:]))
	}
	os.Exit(m.Run())
}

// The binary's contract for a Kubernetes Job: exit code and a JSON error line.
func TestExitCodes(t *testing.T) {
	bogus := filepath.Join(t.TempDir(), "x.mkv")
	if err := os.WriteFile(bogus, []byte("nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		args []string
		code int
	}{
		{"no arguments", nil, 2},
		{"unknown flag", []string{"--bogus"}, 2},
		{"not media", []string{"--input", bogus, "--output", filepath.Join(t.TempDir(), "out")}, 1},
		{"movie id is not a UUID", []string{"--movie-id", "../../etc"}, 2},
		{"movie id with --input", []string{"--movie-id", "0b9d6b52-2d2c-4a7b-9e0e-6a4f39a0c1d2", "--input", bogus}, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := exec.CommandContext(t.Context(), os.Args[0], tt.args...) // #nosec G204 G702 -- re-runs this test binary with fixed test-case args
			cmd.Env = append(os.Environ(), runMainEnv+"=1")
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			err := cmd.Run()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != tt.code {
				t.Fatalf("exit = %v, want code %d\nstderr:\n%s", err, tt.code, stderr.String())
			}
			if tt.code == 1 {
				last := lastJSONLine(t, stderr.String())
				if last.Level != "ERROR" || last.Msg != "transcode failed" || last.Err == "" {
					t.Fatalf("last log line = %+v, want an ERROR \"transcode failed\" with err", last)
				}
			}
		})
	}
}

type logLine struct {
	Level string `json:"level"`
	Msg   string `json:"msg"`
	Err   string `json:"err"`
}

func lastJSONLine(t *testing.T, s string) logLine {
	t.Helper()
	var last logLine
	for _, l := range strings.Split(strings.TrimSpace(s), "\n") {
		if strings.HasPrefix(l, "{") {
			if err := json.Unmarshal([]byte(l), &last); err != nil {
				t.Fatalf("log line is not JSON: %q", l)
			}
		}
	}
	return last
}
