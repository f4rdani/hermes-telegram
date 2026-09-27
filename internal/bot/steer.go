package bot

import (
	"fmt"
	"strings"
	"time"
)

// setPendingSteer stores steer text delivered as the next turn after the running task ends
// (mirrors gateway leftover-steer: "deliver as the next user turn").
func (s *BotServer) setPendingSteer(userID int64, text string) {
	s.steerMu.Lock()
	defer s.steerMu.Unlock()
	s.pendingSteer[userID] = text
}

func (s *BotServer) takePendingSteer(userID int64) string {
	s.steerMu.Lock()
	defer s.steerMu.Unlock()
	t := s.pendingSteer[userID]
	delete(s.pendingSteer, userID)
	return t
}

func (s *BotServer) handleSteerCommand(chatID, userID int64, arg string) {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		sent, _ := SendSafeMessage(s.bot, chatID, "🧭 *Steer Agent:*\n\nFormat: `/steer <arahan tindakan berikutnya>`\nMenyuntik arahan ke tugas yang sedang berjalan — dikirim sebagai turn lanjutan begitu tool call saat ini selesai.", nil)
		s.sessMgr.AddTelegramMsgID(userID, sent.MessageID)
		return
	}
	if s.runner.IsRunning(userID) {
		s.setPendingSteer(userID, arg)
		sent, _ := SendSafeMessage(s.bot, chatID, fmt.Sprintf("🧭 *Steer Tersimpan:*\n`%s`\n\n_Akan disuntik sebagai turn lanjutan begitu giliran saat ini selesai._", truncate(arg, 80)), nil)
		s.sessMgr.AddTelegramMsgID(userID, sent.MessageID)
		return
	}
	// Idle: same as queueing (nothing to steer into).
	s.handleQueueCommand(chatID, userID, arg)
}

// maybeInjectSteerFollowup moves a stored steer to the front of the queue so it
// runs immediately after the just-finished task. Returns true if injected.
func (s *BotServer) maybeInjectSteerFollowup(chatID, userID int64) bool {
	steer := s.takePendingSteer(userID)
	if strings.TrimSpace(steer) == "" {
		return false
	}
	s.pushFrontTask(QueuedTask{
		ChatID:     chatID,
		UserID:     userID,
		Prompt:     fmt.Sprintf("[Steer — arahan susulan saat tugas berjalan]: %s", steer),
		EnqueuedAt: time.Now(),
	})
	return true
}
