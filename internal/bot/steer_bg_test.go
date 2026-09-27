package bot

import (
	"strings"
	"testing"
)

func newSteerTestServer() *BotServer {
	return &BotServer{
		promptQueue:  make(map[int64][]QueuedTask),
		pendingSteer: make(map[int64]string),
		bgTasks:      make(map[string]*BackgroundTask),
	}
}

func TestSteerStoreTake(t *testing.T) {
	s := newSteerTestServer()
	const u int64 = 7
	if got := s.takePendingSteer(u); got != "" {
		t.Fatal("expected empty")
	}
	s.setPendingSteer(u, "jangan restart")
	if got := s.takePendingSteer(u); got != "jangan restart" {
		t.Fatalf("got %q", got)
	}
	if got := s.takePendingSteer(u); got != "" {
		t.Fatal("take must clear")
	}
}

func TestSteerFollowupGoesFront(t *testing.T) {
	s := newSteerTestServer()
	const u int64 = 7
	seedQueue(s, u, "antrean-lama")
	s.setPendingSteer(u, "cek dulu sebelum lanjut")
	if !s.maybeInjectSteerFollowup(1, u) {
		t.Fatal("expected injection")
	}
	q := s.listQueue(u)
	if len(q) != 2 {
		t.Fatalf("queue len = %d", len(q))
	}
	if got := q[0].Prompt; !strings.HasPrefix(got, "[Steer") {
		t.Fatalf("front item should be steer followup, got %q", got)
	}
	if s.maybeInjectSteerFollowup(1, u) {
		t.Fatal("second inject must be false (consumed)")
	}
}

func TestBgCapCountsRunning(t *testing.T) {
	s := newSteerTestServer()
	const u int64 = 9
	s.bgTasks["bg_a"] = &BackgroundTask{ID: "bg_a", UserID: u, Status: "running"}
	s.bgTasks["bg_b"] = &BackgroundTask{ID: "bg_b", UserID: u, Status: "done"}
	s.bgMu.Lock()
	n := s.countUserBgRunningLocked(u)
	s.bgMu.Unlock()
	if n != 1 {
		t.Fatalf("running count = %d, want 1", n)
	}
	if lines := s.listBackgroundLines(); len(lines) != 2 {
		t.Fatalf("lines = %d, want 2", len(lines))
	}
}
