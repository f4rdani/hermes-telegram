package bot

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"hermes-tele/internal/engine"
)

// maxBackgroundPerUser caps parallel background tasks (2GB RAM box).
const maxBackgroundPerUser = 3

// BackgroundTask is a Hermes run in its own fresh session, independent of the
// user's foreground chat (mirrors gateway /bg: separate session, result posted here).
type BackgroundTask struct {
	ID        string
	UserID    int64
	ChatID    int64
	Prompt    string
	SessionID string // fresh Hermes session created by the run ("" until known)
	Status    string // running | done | failed | cancelled
	StartedAt time.Time
	EndedAt   time.Time
	runKey    string
}

func bgRunKey(id string) string {
	return "bg:" + id
}

// countUserBgRunning must be called with s.bgMu held.
func (s *BotServer) countUserBgRunningLocked(userID int64) int {
	n := 0
	for _, t := range s.bgTasks {
		if t.UserID == userID && t.Status == "running" {
			n++
		}
	}
	return n
}

func (s *BotServer) handleBgCommand(chatID, userID int64, arg string) {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		sent, _ := SendSafeMessage(s.bot, chatID, "⚙️ *Tugas Latar Belakang (Background Task):*\n\nFormat: `/bg <instruksi tugas>`\nBerjalan di sesi Hermes terpisah — kamu bisa lanjut chat, hasilnya dikirim saat selesai.", nil)
		s.sessMgr.AddTelegramMsgID(userID, sent.MessageID)
		return
	}

	s.bgMu.Lock()
	if s.countUserBgRunningLocked(userID) >= maxBackgroundPerUser {
		s.bgMu.Unlock()
		sent, _ := SendSafeMessage(s.bot, chatID, fmt.Sprintf("⚠️ *Batas Background Tercapai:*\nMaksimal %d tugas latar berjalan per user. Tunggu selesai atau batalkan via `/stop`.", maxBackgroundPerUser), nil)
		s.sessMgr.AddTelegramMsgID(userID, sent.MessageID)
		return
	}
	s.bgSeq++
	task := &BackgroundTask{
		ID:        fmt.Sprintf("bg_%s_%d", time.Now().Format("150405"), s.bgSeq),
		UserID:    userID,
		ChatID:    chatID,
		Prompt:    arg,
		Status:    "running",
		StartedAt: time.Now(),
	}
	task.runKey = bgRunKey(task.ID)
	if s.bgTasks == nil {
		s.bgTasks = make(map[string]*BackgroundTask)
	}
	s.bgTasks[task.ID] = task
	s.bgMu.Unlock()

	notice, _ := SendSafeMessage(s.bot, chatID, fmt.Sprintf("🔄 *Background Task Dimulai:*\n🆔 `%s`\n`%s`\n\n_Kamu bisa lanjut chat — hasilnya dikirim di sini saat selesai._", task.ID, truncate(arg, 80)), nil)
	s.sessMgr.AddTelegramMsgID(userID, notice.MessageID)
	go s.executeBackgroundTask(task)
}

func (s *BotServer) executeBackgroundTask(task *BackgroundTask) {
	userID, chatID := task.UserID, task.ChatID
	userSess := s.sessMgr.Get(userID, chatID)

	cancelKb := CancelKeyboard()
	statusMsg, err := SendSafeMessage(s.bot, chatID, fmt.Sprintf("🔄 *Background `%s` berjalan...*\n\n💭 _Sedang berpikir & menganalisis instruksi..._", task.ID), cancelKb)
	hasStatusMsg := err == nil
	if hasStatusMsg {
		s.sessMgr.AddTelegramMsgID(userID, statusMsg.MessageID)
	}

	stopTyping := make(chan struct{})
	go func() {
		ticker := time.NewTicker(4 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stopTyping:
				return
			case <-ticker.C:
				_, _ = s.bot.Send(tgbotapi.NewChatAction(chatID, tgbotapi.ChatTyping))
			}
		}
	}()

	startTime := time.Now()
	log.Printf("[bg] Task %s starting for user %d (fresh session, model=%s)", task.ID, userID, userSess.CurrentModel)

	inactTimeout := 5 * time.Minute
	if s.cfg.Hermes.InactivityTimeoutSec > 0 {
		inactTimeout = time.Duration(s.cfg.Hermes.InactivityTimeoutSec) * time.Second
	}
	hardTimeout := 2 * time.Hour
	if s.cfg.Hermes.MaxDurationSec > 0 {
		hardTimeout = time.Duration(s.cfg.Hermes.MaxDurationSec) * time.Second
	}

	opts := engine.RunOptions{
		Prompt:            task.Prompt,
		SessionID:         "", // fresh session — never touches the foreground chat history
		Model:             userSess.CurrentModel,
		ReasoningEffort:   userSess.ReasoningEffort,
		Yolo:              userSess.YoloMode,
		WorkingDir:        s.cfg.Hermes.WorkingDir,
		Timeout:           hardTimeout,
		InactivityTimeout: inactTimeout,
		OnProgress: func(displayText string) {
			if hasStatusMsg {
				_, _ = EditSafeMessage(s.bot, chatID, statusMsg.MessageID, fmt.Sprintf("🔄 *Background `%s`*\n\n%s", task.ID, displayText), &cancelKb)
			}
			if isSelfRestartCriticalText(displayText) {
				s.sendSelfRestartApprovalCard(chatID, userID, task.runKey, displayText)
			}
		},
	}

	result, runErr := s.runner.ExecuteWithKey(context.Background(), task.runKey, userID, opts)
	close(stopTyping)
	duration := time.Since(startTime).Round(time.Millisecond)

	s.bgMu.Lock()
	if task.Status == "running" {
		if runErr != nil {
			task.Status = "failed"
		} else {
			task.Status = "done"
		}
	}
	task.EndedAt = time.Now()
	if result != nil && result.SessionID != "" {
		task.SessionID = result.SessionID
	}
	s.bgMu.Unlock()

	if runErr != nil {
		log.Printf("[bg] Task %s error (%v): %v", task.ID, duration, runErr)
		errText := fmt.Sprintf("❌ *Background `%s` Gagal (%v):*\n```\n%v\n```", task.ID, duration, runErr)
		if hasStatusMsg {
			_, _ = EditSafeMessage(s.bot, chatID, statusMsg.MessageID, errText, nil)
		} else {
			sent, _ := SendSafeMessage(s.bot, chatID, errText, nil)
			s.sessMgr.AddTelegramMsgID(userID, sent.MessageID)
		}
		return
	}

	finalText := ""
	if result != nil {
		finalText = strings.TrimSpace(result.FinalText)
	}
	if finalText == "" {
		finalText = fmt.Sprintf("✅ *Background `%s` selesai dalam %v tanpa keluaran teks.*", task.ID, duration)
	} else {
		finalText = fmt.Sprintf("✅ *Background `%s` Selesai:*\n\n%s", task.ID, finalText)
	}
	finalText = s.ProcessOutboundMedia(chatID, userID, finalText)

	activeSessionID := ""
	if result != nil {
		activeSessionID = result.SessionID
	}
	footer := s.formatResultFooter(duration, result, activeSessionID, userSess.CurrentModel)
	fullResponse := finalText + footer

	chunks := SplitMessage(fullResponse, 30000)
	if hasStatusMsg && len(chunks) > 0 {
		_, _ = EditSafeMessage(s.bot, chatID, statusMsg.MessageID, chunks[0], nil)
		for _, chunk := range chunks[1:] {
			sent, _ := SendSafeMessage(s.bot, chatID, chunk, nil)
			s.sessMgr.AddTelegramMsgID(userID, sent.MessageID)
		}
	} else {
		for _, chunk := range chunks {
			sent, _ := SendSafeMessage(s.bot, chatID, chunk, nil)
			s.sessMgr.AddTelegramMsgID(userID, sent.MessageID)
		}
	}
	log.Printf("[bg] Task %s completed in %v (chunks: %d)", task.ID, duration, len(chunks))
}

// listBackgroundLines renders registry lines for /ps (running first).
func (s *BotServer) listBackgroundLines() []string {
	s.bgMu.Lock()
	defer s.bgMu.Unlock()
	var running, rest []string
	for _, t := range s.bgTasks {
		elapsed := t.StartedAt.Format("15:04:05")
		line := fmt.Sprintf("• `%s` [%s] mulai %s:\n  `%s`", t.ID, t.Status, elapsed, truncate(t.Prompt, 60))
		if t.Status == "running" {
			running = append(running, line)
		} else {
			rest = append(rest, line)
		}
	}
	out := append(running, rest...)
	if len(out) > 6 {
		out = out[:6]
	}
	return out
}

// stopUserBackgroundTasks cancels all running bg tasks of a user. Returns count stopped.
func (s *BotServer) stopUserBackgroundTasks(userID int64) int {
	s.bgMu.Lock()
	var keys []string
	for _, t := range s.bgTasks {
		if t.UserID == userID && t.Status == "running" {
			t.Status = "cancelled"
			t.EndedAt = time.Now()
			keys = append(keys, t.runKey)
		}
	}
	s.bgMu.Unlock()
	n := 0
	for _, k := range keys {
		if s.runner.StopKey(k) {
			n++
		} else {
			n++ // marked cancelled even if the run slot already exited
		}
	}
	return len(keys)
}
