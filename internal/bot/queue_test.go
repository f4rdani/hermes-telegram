package bot

import (
	"testing"
	"time"
)

func newQueueTestServer() *BotServer {
	return &BotServer{promptQueue: make(map[int64][]QueuedTask)}
}

func seedQueue(s *BotServer, userID int64, prompts ...string) {
	for _, p := range prompts {
		s.enqueueTask(QueuedTask{ChatID: 1, UserID: userID, Prompt: p, EnqueuedAt: time.Now()})
	}
}

func queuePrompts(s *BotServer, userID int64) []string {
	q := s.listQueue(userID)
	out := make([]string, len(q))
	for i, t := range q {
		out[i] = t.Prompt
	}
	return out
}

func equalStr(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestQueueEditRmMove(t *testing.T) {
	s := newQueueTestServer()
	const u int64 = 42
	seedQueue(s, u, "satu", "dua", "tiga")

	if !s.queueEdit(u, 1, "DUA") {
		t.Fatal("edit should succeed")
	}
	if got := queuePrompts(s, u); !equalStr(got, []string{"satu", "DUA", "tiga"}) {
		t.Fatalf("after edit: %v", got)
	}

	if !s.queueMove(u, 2, 0) {
		t.Fatal("move should succeed")
	}
	if got := queuePrompts(s, u); !equalStr(got, []string{"tiga", "satu", "DUA"}) {
		t.Fatalf("after move: %v", got)
	}

	removed, ok := s.queueRemove(u, 1)
	if !ok || removed.Prompt != "satu" {
		t.Fatalf("rm got %q, %v", removed.Prompt, ok)
	}
	if got := queuePrompts(s, u); !equalStr(got, []string{"tiga", "DUA"}) {
		t.Fatalf("after rm: %v", got)
	}

	if s.queueEdit(u, 9, "x") || s.queueMove(u, 0, 9) {
		t.Fatal("out-of-range ops must fail")
	}
	if _, ok := s.queueRemove(u, -1); ok {
		t.Fatal("negative rm must fail")
	}
	if n := s.clearQueue(u); n != 2 {
		t.Fatalf("clear count = %d, want 2", n)
	}
}
