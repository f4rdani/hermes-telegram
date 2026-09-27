package bot

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"regexp"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// ApprovalTimeout is the fail-closed window: silence means the command does NOT run.
const ApprovalTimeout = 60 * time.Second

// PendingApproval is one self-restart approval request awaiting the owner's decision.
// Mirrors the Hermes Python gateway exec-approval semantics: Allow Once / Deny / timeout→deny.
type PendingApproval struct {
	RequestID string
	UserID    int64
	ChatID    int64
	// RunKey is the exact runner slot to kill on deny/timeout
	// (foreground "user:N" or background "bg:<id>").
	RunKey    string
	CardMsgID int
	Command   string // redacted preview shown on the card
	Reason    string
	CreatedAt time.Time

	mu      sync.Mutex
	settled bool
	timer   *time.Timer
}

// ApprovalStore keeps at most one live approval per user.
type ApprovalStore struct {
	mu      sync.Mutex
	timeout time.Duration
	byUser  map[int64]*PendingApproval
	byID    map[string]*PendingApproval
}

func NewApprovalStore(timeout time.Duration) *ApprovalStore {
	if timeout <= 0 {
		timeout = ApprovalTimeout
	}
	return &ApprovalStore{
		timeout: timeout,
		byUser:  make(map[int64]*PendingApproval),
		byID:    make(map[string]*PendingApproval),
	}
}

func newRequestID() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// isSelfRestartCriticalText reports the actual suicide pattern: systemctl touching hermes-tele.
// go build alone only produces a warning (harmless by itself); the kill happens at restart.
func isSelfRestartCriticalText(displayText string) bool {
	lower := strings.ToLower(displayText)
	return strings.Contains(lower, "systemctl") && strings.Contains(lower, "hermes-tele")
}

// selfRestartReason explains why the command was flagged (gateway-style "why it was flagged").
func selfRestartReason(commandPreview string) string {
	lower := strings.ToLower(commandPreview)
	switch {
	case strings.Contains(lower, "restart") && strings.Contains(lower, "hermes-tele"):
		return "self-restart bot dari dalam tugasnya sendiri (proses bisa mati sebelum membalas)"
	case strings.Contains(lower, "stop") && strings.Contains(lower, "hermes-tele"):
		return "menghentikan layanan bot dari dalam tugasnya sendiri"
	case strings.Contains(lower, "systemctl") && strings.Contains(lower, "hermes-tele"):
		return "mengubah layanan systemd hermes-tele dari dalam tugasnya sendiri"
	default:
		return "perintah berbahaya terhadap layanan hermes-tele"
	}
}

var (
	backtickCmdRegex = regexp.MustCompile("`([^`]{1,300})`")
	botTokenRegex    = regexp.MustCompile(`\b\d{6,12}:[A-Za-z0-9_-]{30,}\b`)
	apiKeyRegex      = regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{8,}\b`)
)

// redactSecrets masks tokens/keys before they ever reach Telegram.
func redactSecrets(s string) string {
	s = botTokenRegex.ReplaceAllString(s, "***REDACTED-BOT-TOKEN***")
	s = apiKeyRegex.ReplaceAllString(s, "***REDACTED-KEY***")
	return s
}

// extractCommandPreview pulls the command out of the tool-activity line for the approval card.
func extractCommandPreview(displayText string) string {
	if m := backtickCmdRegex.FindStringSubmatch(displayText); len(m) > 1 {
		cmd := strings.TrimSpace(m[1])
		cmd = strings.ReplaceAll(cmd, "\n", " ")
		if len([]rune(cmd)) > 200 {
			cmd = string([]rune(cmd)[:200]) + "..."
		}
		return redactSecrets(cmd)
	}
	flat := strings.ReplaceAll(displayText, "\n", " ")
	flat = strings.TrimSpace(flat)
	if len([]rune(flat)) > 200 {
		flat = string([]rune(flat)[:200]) + "..."
	}
	return redactSecrets(flat)
}

// formatApprovalCard renders the gateway-style approval prompt (ID to match Hermes wording).
func formatApprovalCard(command, reason string, timeout time.Duration) string {
	secs := int(timeout.Seconds())
	deadline := fmt.Sprintf("%d detik", secs)
	if secs >= 60 {
		deadline = fmt.Sprintf("%d menit", secs/60)
	}
	return fmt.Sprintf("⚠️ *Hermes ingin menjalankan perintah yang butuh persetujuanmu*\n\n"+
		"```\n%s\n```\n*Alasan ditandai:* %s\n\n"+
		"✅ *Izinkan Sekali* = batalkan tugas Hermes ini, lalu restart lewat jalur aman (ada notifikasi online otomatis).\n"+
		"❌ *Tolak* = batalkan tugas Hermes ini, perintah TIDAK dijalankan.\n\n"+
		"_Jika tidak menjawab dalam %s, perintah TIDAK dijalankan._\n"+
		"_Bisa juga ketik /approve atau /deny._",
		command, reason, deadline)
}

// SelfRestartApprovalKeyboard renders native approve/deny buttons for a pending request.
func SelfRestartApprovalKeyboard(requestID string) tgbotapi.InlineKeyboardMarkup {
	return tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("✅ Izinkan Sekali", "approve:sr:"+requestID),
			tgbotapi.NewInlineKeyboardButtonData("❌ Tolak", "deny:sr:"+requestID),
		),
	)
}

// Create registers a pending approval for the user (single live entry).
// Returns (pending, isNew). If a live entry exists, it is returned with isNew=false.
func (a *ApprovalStore) Create(userID, chatID int64, runKey, command, reason string, onTimeout func(*PendingApproval)) (*PendingApproval, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if p, ok := a.byUser[userID]; ok && !p.isSettled() {
		return p, false
	}
	p := &PendingApproval{
		RequestID: newRequestID(),
		UserID:    userID,
		ChatID:    chatID,
		RunKey:    runKey,
		Command:   command,
		Reason:    reason,
		CreatedAt: time.Now(),
	}
	if onTimeout != nil {
		p.timer = time.AfterFunc(a.timeout, func() { onTimeout(p) })
	}
	a.byUser[userID] = p
	a.byID[p.RequestID] = p
	return p, true
}

// Settle marks a request resolved and removes it. Returns the entry or nil if unknown/expired.
func (a *ApprovalStore) Settle(requestID string) *PendingApproval {
	a.mu.Lock()
	defer a.mu.Unlock()
	p, ok := a.byID[requestID]
	if !ok || p.isSettled() {
		return nil
	}
	p.settleLocked()
	delete(a.byID, requestID)
	if cur, ok := a.byUser[p.UserID]; ok && cur == p {
		delete(a.byUser, p.UserID)
	}
	return p
}

// SettleUser resolves the live entry for a user (for /approve, /deny, /stop, task end).
func (a *ApprovalStore) SettleUser(userID int64) *PendingApproval {
	a.mu.Lock()
	defer a.mu.Unlock()
	p, ok := a.byUser[userID]
	if !ok || p.isSettled() {
		return nil
	}
	p.settleLocked()
	delete(a.byID, p.RequestID)
	delete(a.byUser, userID)
	return p
}

// Get returns the live entry for a user, or nil.
func (a *ApprovalStore) Get(userID int64) *PendingApproval {
	a.mu.Lock()
	defer a.mu.Unlock()
	if p, ok := a.byUser[userID]; ok && !p.isSettled() {
		return p
	}
	return nil
}

func (p *PendingApproval) isSettled() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.settled
}

func (p *PendingApproval) settleLocked() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.settled {
		return
	}
	p.settled = true
	if p.timer != nil {
		p.timer.Stop()
	}
}

// --- BotServer wiring -----------------------------------------------------------

// handleApprovalTimeout is the fail-closed path: silence for 60s means the command does NOT run.
func (s *BotServer) handleApprovalTimeout(p *PendingApproval) {
	gone := s.approvals.Settle(p.RequestID)
	if gone == nil {
		return // already resolved via button or /approve /deny
	}
	// Best effort: kill the exact run slot before `sleep 3 && systemctl restart` fires.
	stopped := s.runner.StopKey(p.RunKey)
	notice := fmt.Sprintf("⌛ *Persetujuan kedaluwarsa (60 detik) — perintah TIDAK dijalankan.*\n\n```\n%s\n```\n_Minta Aida untuk mencoba lagi jika tetap dibutuhkan, atau pakai /restart._", p.Command)
	_, _ = EditSafeMessage(s.bot, p.ChatID, p.CardMsgID, notice, nil)
	log.Printf("[approval] Self-restart %s timed out for user %d (task stopped: %v)", p.RequestID, p.UserID, stopped)
}

// sendSelfRestartApprovalCard creates the pending entry and posts the button card.
// runKey is the exact runner slot ("user:N" foreground or "bg:<id>") to kill on deny/timeout.
// Returns true if a card is now live (new or already existing).
func (s *BotServer) sendSelfRestartApprovalCard(chatID, userID int64, runKey, displayText string) bool {
	preview := extractCommandPreview(displayText)
	reason := selfRestartReason(preview)
	p, isNew := s.approvals.Create(userID, chatID, runKey, preview, reason, s.handleApprovalTimeout)
	if !isNew {
		return true
	}
	kb := SelfRestartApprovalKeyboard(p.RequestID)
	sent, err := SendSafeMessage(s.bot, chatID, formatApprovalCard(preview, reason, ApprovalTimeout), kb)
	if err != nil {
		s.approvals.Settle(p.RequestID)
		log.Printf("[approval] Failed to send approval card for user %d: %v", userID, err)
		return false
	}
	p.CardMsgID = sent.MessageID
	s.sessMgr.AddTelegramMsgID(userID, sent.MessageID)
	log.Printf("[approval] Self-restart card %s sent to user %d: %q", p.RequestID, userID, preview)
	return true
}

// resolveSelfRestartApproval settles the card and executes the decision.
// approve=true takes over the restart via the safe flow (pending-notify + online notice);
// deny/timeout kills the Hermes task so the in-band restart never fires.
func (s *BotServer) resolveSelfRestartApproval(cbID string, chatID int64, requestID string, approved bool, denyReason string) {
	answer := func(text string) {
		if cbID != "" {
			_, _ = s.bot.Request(tgbotapi.NewCallback(cbID, text))
		}
	}
	p := s.approvals.Settle(requestID)
	if p == nil {
		answer("⌛ Sudah kedaluwarsa / tidak ada approval tertunda")
		return
	}
	if approved {
		answer("✅ Diizinkan — restart via jalur aman")
		_, _ = EditSafeMessage(s.bot, chatID, p.CardMsgID, fmt.Sprintf("✅ *Disetujui — restart via jalur aman.*\n\n```\n%s\n```\n_Membatalkan tugas Hermes ini agar tidak self-restart, lalu restart dengan notifikasi online otomatis..._", p.Command), nil)
		// Stop the in-band slot first so Hermes cannot suicide mid-restart.
		s.aggregator.Cancel(p.UserID)
		s.runner.StopKey(p.RunKey)
		time.Sleep(600 * time.Millisecond)
		s.cmdHandler.HandleRestart(s.bot, chatID)
		log.Printf("[approval] Self-restart %s approved by user %d (safe restart taken over)", requestID, p.UserID)
		return
	}
	answer("❌ Ditolak")
	text := fmt.Sprintf("❌ *Ditolak — perintah TIDAK dijalankan.*\n\n```\n%s\n```", p.Command)
	if strings.TrimSpace(denyReason) != "" {
		text += fmt.Sprintf("\n_Alasan: %s_", denyReason)
	}
	_, _ = EditSafeMessage(s.bot, chatID, p.CardMsgID, text, nil)
	s.aggregator.Cancel(p.UserID)
	stopped := s.runner.StopKey(p.RunKey)
	log.Printf("[approval] Self-restart %s denied by user %d (task stopped: %v, reason: %q)", requestID, p.UserID, stopped, denyReason)
}

// withdrawSelfRestartApproval closes a still-open card without running anything
// (used when the task ends on its own).
func (s *BotServer) withdrawSelfRestartApproval(userID int64, note string) {
	p := s.approvals.SettleUser(userID)
	if p == nil || p.CardMsgID == 0 {
		return
	}
	if note == "" {
		note = "Tugas selesai — tidak ada perintah self-restart yang dijalankan."
	}
	_, _ = EditSafeMessage(s.bot, p.ChatID, p.CardMsgID, fmt.Sprintf("ℹ️ *Approval ditutup.*\n\n_%s_", note), nil)
}

// handleApproveTextCommand resolves a pending self-restart via typed /approve.
func (s *BotServer) handleApproveTextCommand(chatID, userID int64, arg string) {
	p := s.approvals.Get(userID)
	if p == nil {
		// No pending card — fall back to the informational status.
		s.cmdHandler.HandleApprove(s.bot, chatID, userID, arg)
		return
	}
	s.resolveSelfRestartApproval("", chatID, p.RequestID, true, "")
}

// handleDenyTextCommand resolves a pending self-restart via typed /deny [reason].
func (s *BotServer) handleDenyTextCommand(chatID, userID int64, arg string) {
	p := s.approvals.Get(userID)
	if p == nil {
		s.cmdHandler.HandleDeny(s.bot, chatID, userID, arg)
		return
	}
	s.resolveSelfRestartApproval("", chatID, p.RequestID, false, arg)
}
