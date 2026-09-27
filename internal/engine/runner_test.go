package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRunnerInactivityTimeout(t *testing.T) {
	tmpDir := t.TempDir()
	dummyBin := filepath.Join(tmpDir, "dummy_hermes")

	// Dummy script that prints init event, and then sleeps for 4 seconds without any output
	script := `#!/bin/sh
echo '{"type":"system","subtype":"init","session_id":"test_sess_123"}'
sleep 4
echo '{"type":"result","session_id":"test_sess_123","exit_code":0,"text":"done"}'
`
	if err := os.WriteFile(dummyBin, []byte(script), 0755); err != nil {
		t.Fatalf("failed to create dummy script: %v", err)
	}

	runner := NewRunner(dummyBin)

	// Inactivity timeout set to 1.5 seconds. The script will sleep 4 seconds, so it must trigger ErrInactivityTimeout!
	opts := RunOptions{
		Prompt:            "test prompt",
		InactivityTimeout: 1500 * time.Millisecond,
		Timeout:           10 * time.Second,
	}

	start := time.Now()
	res, err := runner.Execute(context.Background(), 12345, opts)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatalf("expected InactivityTimeout error, got nil")
	}

	if !errors.Is(err, ErrInactivityTimeout) {
		t.Errorf("expected ErrInactivityTimeout, got: %v", err)
	}

	if res == nil || !res.IsStuck {
		t.Errorf("expected res.IsStuck to be true, got: %+v", res)
	}

	if elapsed > 3500*time.Millisecond {
		t.Errorf("watchdog took too long to kill stuck process: %v", elapsed)
	}
}

func TestRunnerActivityResetsTimeout(t *testing.T) {
	tmpDir := t.TempDir()
	dummyBin := filepath.Join(tmpDir, "dummy_hermes_active")

	// Dummy script that emits events every 800ms for 3 seconds.
	// Inactivity timeout is 1.5s. Because events arrive every 800ms, it should NEVER trigger inactivity timeout!
	script := `#!/bin/sh
echo '{"type":"system","subtype":"init","session_id":"test_sess_active"}'
sleep 0.8
echo '{"type":"text","text":"chunk 1"}'
sleep 0.8
echo '{"type":"text","text":"chunk 2"}'
sleep 0.8
echo '{"type":"result","session_id":"test_sess_active","exit_code":0,"text":"all done"}'
`
	if err := os.WriteFile(dummyBin, []byte(script), 0755); err != nil {
		t.Fatalf("failed to create dummy script: %v", err)
	}

	runner := NewRunner(dummyBin)

	opts := RunOptions{
		Prompt:            "test prompt",
		InactivityTimeout: 1500 * time.Millisecond,
		Timeout:           10 * time.Second,
	}

	res, err := runner.Execute(context.Background(), 12345, opts)
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}

	if res == nil || res.IsStuck {
		t.Errorf("expected clean completion, got res: %+v", res)
	}

	if res.FinalText != "all done" {
		t.Errorf("expected 'all done', got: %q", res.FinalText)
	}
}
