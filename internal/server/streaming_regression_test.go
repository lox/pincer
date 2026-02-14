package server

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"testing"
	"time"

	protocolv1 "github.com/lox/pincer/gen/proto/pincer/protocol/v1"
)

func TestPublishThreadEventDoesNotDropWhenSubscriberBufferFull(t *testing.T) {
	app := newTestApp(t)
	threadID := "thr_publish_no_drop"

	sub := app.subscribeThread(threadID)
	defer app.unsubscribeThread(threadID, sub)

	for i := 0; i < cap(sub); i++ {
		sub <- &threadEvent{
			event: &protocolv1.ThreadEvent{
				EventId:  fmt.Sprintf("dummy-%d", i),
				ThreadId: threadID,
			},
		}
	}

	const targetID = "evt_target"
	app.publishThreadEvent(&protocolv1.ThreadEvent{
		EventId:  targetID,
		ThreadId: threadID,
	})

	// Make room for one additional delivery.
	<-sub

	deadline := time.After(2 * time.Second)
	for {
		select {
		case incoming := <-sub:
			if incoming != nil && incoming.event != nil && incoming.event.GetEventId() == targetID {
				return
			}
		case <-deadline:
			t.Fatalf("expected buffered target event %q to be delivered", targetID)
		}
	}
}

func TestRunBashCommandStreamingStartFailureDoesNotLeakFDs(t *testing.T) {
	oldGCPercent := debug.SetGCPercent(-1)
	defer func() {
		_ = debug.SetGCPercent(oldGCPercent)
		runtime.GC()
	}()

	baseline := countOpenFDsOrSkip(t)
	missingDir := filepath.Join(t.TempDir(), "missing")

	for i := 0; i < 40; i++ {
		output, _, exitCode, _, _ := runBashCommandStreaming("echo hello", missingDir, nil)
		if exitCode != -1 {
			t.Fatalf("expected exit_code -1 on start failure, got %d", exitCode)
		}
		if !strings.Contains(output, "failed to start bash command") {
			t.Fatalf("expected start failure output, got %q", output)
		}
	}

	after := countOpenFDsOrSkip(t)
	if diff := after - baseline; diff > 8 {
		t.Fatalf("expected no fd leak on start failures, open fd diff=%d (baseline=%d after=%d)", diff, baseline, after)
	}
}

func countOpenFDsOrSkip(t *testing.T) int {
	t.Helper()

	entries, err := os.ReadDir("/dev/fd")
	if err != nil {
		t.Skipf("fd counting unsupported on this platform: %v", err)
	}
	return len(entries)
}
