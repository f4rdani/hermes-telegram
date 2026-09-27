package bot

import (
	"strings"
	"testing"
	"time"
)

func TestIsSelfRestartCriticalText(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"💻 Bash / Terminal: `systemctl restart hermes-tele && sleep 3 && s...`", true},
		{"💻 Bash / Terminal: `SYSTEMCTL STOP hermes-tele.service`", true},
		{"💻 Bash / Terminal: `cd /root/apps/hermes-tele && go build -ldflag...`", false}, // warn-only tier
		{"💻 Bash / Terminal: `ffmpeg -i in.mp4 out.mp4`", false},
		{"📂 Membaca File: `/root/apps/hermes-tele/internal/bot/media_out...`", false},
		{"⚡ Status: Menyusun jawaban...", false},
	}
	for _, c := range cases {
		if got := isSelfRestartCriticalText(c.in); got != c.want {
			t.Errorf("isSelfRestartCriticalText(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestExtractCommandPreviewRedactsSecrets(t *testing.T) {
	in := "⚡ Sedang dijalankan: 💻 *Bash / Terminal*: `systemctl restart hermes-tele --token 1234567890:AAEcDeFgHiJkLmNoPqRsTuVwXyZ0123456789abc`"
	out := extractCommandPreview(in)
	if strings.Contains(out, "AAEcDeFgHiJkLmNoPqRsTuVwXyZ0123456789abc") {
		t.Errorf("bot token leaked in preview: %q", out)
	}
	if !strings.Contains(out, "systemctl restart hermes-tele") {
		t.Errorf("command lost in preview: %q", out)
	}
}

func TestApprovalStoreSinglePendingAndSettle(t *testing.T) {
	store := NewApprovalStore(time.Minute)
	p1, isNew := store.Create(1, 100, "user:1", "systemctl restart hermes-tele", "reason", nil)
	if !isNew || p1 == nil {
		t.Fatal("first create should be new")
	}
	p2, isNew := store.Create(1, 100, "user:1", "systemctl stop hermes-tele", "reason", nil)
	if isNew || p2.RequestID != p1.RequestID {
		t.Fatal("second create should return existing pending")
	}
	if got := store.Settle(p1.RequestID); got == nil {
		t.Fatal("settle should resolve")
	}
	if got := store.Settle(p1.RequestID); got != nil {
		t.Fatal("double settle should return nil")
	}
	if got := store.Get(1); got != nil {
		t.Fatal("no live entry expected after settle")
	}
}

func TestApprovalStoreTimeoutFires(t *testing.T) {
	fired := make(chan string, 1)
	store := NewApprovalStore(30 * time.Millisecond)
	p, _ := store.Create(7, 700, "user:7", "systemctl restart hermes-tele", "reason", func(pp *PendingApproval) {
		fired <- pp.RequestID
	})
	select {
	case id := <-fired:
		if id != p.RequestID {
			t.Fatalf("wrong request settled: %q", id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout callback did not fire")
	}
}
