package bot

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"
)

const pendingRestartNotifyFile = "/root/apps/hermes-tele/.pending_restart_notify.json"

type PendingRestartNotify struct {
	ChatID    int64  `json:"chat_id"`
	MessageID int    `json:"message_id,omitempty"`
	Action    string `json:"action"` // "update" or "restart"
	Timestamp int64  `json:"timestamp"`
	Summary   string `json:"summary,omitempty"`
}

// SavePendingRestart persists restart context to disk so the newly started process can notify the user.
func SavePendingRestart(chatID int64, messageID int, action, summary string) {
	pr := PendingRestartNotify{
		ChatID:    chatID,
		MessageID: messageID,
		Action:    action,
		Timestamp: time.Now().Unix(),
		Summary:   summary,
	}
	data, err := json.Marshal(pr)
	if err != nil {
		log.Printf("[restart] Error marshaling restart notify: %v", err)
		return
	}
	if err := os.WriteFile(pendingRestartNotifyFile, data, 0644); err != nil {
		log.Printf("[restart] Error writing restart notify file: %v", err)
	}
}

// CheckAndNotifyRestart checks if a restart was initiated and informs the user that the bot is back online.
func (s *BotServer) CheckAndNotifyRestart() {
	data, err := os.ReadFile(pendingRestartNotifyFile)
	if err != nil {
		return
	}
	_ = os.Remove(pendingRestartNotifyFile)

	var pr PendingRestartNotify
	if err := json.Unmarshal(data, &pr); err != nil {
		log.Printf("[restart] Error unmarshaling restart notify: %v", err)
		return
	}

	// Ignore stale marker older than 15 minutes
	if time.Now().Unix()-pr.Timestamp > 900 {
		return
	}

	closeKb := CloseKeyboard()

	if pr.Action == "update" {
		// Update previous message if available to show final online status
		if pr.MessageID > 0 && pr.Summary != "" {
			updatedNotice := fmt.Sprintf(
				"✅ *Pembaruan Hermes Agent Selesai!*\n\n```\n%s\n```\n🟢 *Layanan hermes-tele telah online kembali dan siap digunakan.*",
				pr.Summary,
			)
			_, _ = EditSafeMessage(s.bot, pr.ChatID, pr.MessageID, updatedNotice, &closeKb)
		}

		// Send explicit online notification so user receives push notification
		onlineMsg := fmt.Sprintf(
			"🟢 *Hermes Agent & Gateway Siap Digunakan!*\n\n"+
				"✨ Pembaruan sistem dan restart gateway telah selesai dengan sukses.\n"+
				"• *Versi Gateway:* `v%s`\n"+
				"• *Status:* Online & Responsif\n\n"+
				"Silakan kirim pesan atau gunakan perintah `/menu` untuk melanjutkan.",
			s.version,
		)
		sent, err := SendSafeMessage(s.bot, pr.ChatID, onlineMsg, closeKb)
		if err == nil && sent.MessageID != 0 {
			s.sessMgr.AddTelegramMsgID(pr.ChatID, sent.MessageID)
		}
	} else if pr.Action == "restart" {
		if pr.MessageID > 0 {
			_, _ = EditSafeMessage(s.bot, pr.ChatID, pr.MessageID, "🟢 *Gateway Hermes Telegram berhasil dimulai ulang dan siap digunakan kembali.*", &closeKb)
		}
		sent, err := SendSafeMessage(s.bot, pr.ChatID, "🟢 *Gateway Hermes Telegram Siap Digunakan!*\n\nLayanan hermes-tele telah aktif kembali dan siap menerima perintah.", closeKb)
		if err == nil && sent.MessageID != 0 {
			s.sessMgr.AddTelegramMsgID(pr.ChatID, sent.MessageID)
		}
	}
}
