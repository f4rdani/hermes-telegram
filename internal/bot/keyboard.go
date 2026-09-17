package bot

import (
	"fmt"
	"strings"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"hermes-tele/internal/session"
)

func CancelKeyboard() tgbotapi.InlineKeyboardMarkup {
	btn := tgbotapi.NewInlineKeyboardButtonData("🛑 Batalkan Tugas", "cancel_task")
	return tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(btn),
	)
}

func ModelKeyboard(currentModel string) tgbotapi.InlineKeyboardMarkup {
	models := []struct {
		Name  string
		Label string
	}{
		{"9router", "9router (Auto-Combo Default)"},
		{"Es/qwen3.8-flash", "Qwen 3.8 Flash"},
		{"Es/deepseek-v3.2", "DeepSeek V3.2"},
		{"Es/kimi-k3", "Kimi K3 (1M Context)"},
		{"Es/qwen3.6-27b", "Qwen 3.6 27B"},
	}

	var rows [][]tgbotapi.InlineKeyboardButton
	for _, m := range models {
		label := m.Label
		if m.Name == currentModel {
			label = "✓ " + label
		}
		btn := tgbotapi.NewInlineKeyboardButtonData(label, "set_model:"+m.Name)
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(btn))
	}
	return tgbotapi.InlineKeyboardMarkup{InlineKeyboard: rows}
}

func ReasoningKeyboard(currentEffort string) tgbotapi.InlineKeyboardMarkup {
	efforts := []string{"none", "low", "medium", "high"}
	var buttons []tgbotapi.InlineKeyboardButton
	for _, e := range efforts {
		label := strings.ToUpper(e)
		if e == currentEffort {
			label = "✓ " + label
		}
		buttons = append(buttons, tgbotapi.NewInlineKeyboardButtonData(label, "set_reasoning:"+e))
	}
	return tgbotapi.NewInlineKeyboardMarkup(buttons)
}

func SessionsKeyboard(sessions []session.SessionSummary, currentID string) tgbotapi.InlineKeyboardMarkup {
	var rows [][]tgbotapi.InlineKeyboardButton

	for _, s := range sessions {
		prefix := "▫️"
		if s.ID == currentID {
			prefix = "⭐️"
		}
		title := s.Title
		if len([]rune(title)) > 24 {
			title = string([]rune(title)[:24]) + "…"
		}
		btnText := fmt.Sprintf("%s %s (%s)", prefix, title, s.ID[len(s.ID)-6:])
		btn := tgbotapi.NewInlineKeyboardButtonData(btnText, "resume_session:"+s.ID)
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(btn))
	}

	newSessionBtn := tgbotapi.NewInlineKeyboardButtonData("✨ Buat Sesi Baru", "new_session")
	rows = append(rows, tgbotapi.NewInlineKeyboardRow(newSessionBtn))

	return tgbotapi.InlineKeyboardMarkup{InlineKeyboard: rows}
}

func CommandsPaginationKeyboard(page, totalPages int) tgbotapi.InlineKeyboardMarkup {
	var row []tgbotapi.InlineKeyboardButton
	if page > 1 {
		row = append(row, tgbotapi.NewInlineKeyboardButtonData("⬅ Sebelumnya", fmt.Sprintf("cmd_page:%d", page-1)))
	}
	row = append(row, tgbotapi.NewInlineKeyboardButtonData(fmt.Sprintf("%d / %d", page, totalPages), "noop"))
	if page < totalPages {
		row = append(row, tgbotapi.NewInlineKeyboardButtonData("Berikutnya ➡", fmt.Sprintf("cmd_page:%d", page+1)))
	}
	return tgbotapi.NewInlineKeyboardMarkup(row)
}

func ApprovalKeyboard(actionID string) tgbotapi.InlineKeyboardMarkup {
	row := tgbotapi.NewInlineKeyboardRow(
		tgbotapi.NewInlineKeyboardButtonData("✅ Setujui", "approve:"+actionID),
		tgbotapi.NewInlineKeyboardButtonData("❌ Tolak", "deny:"+actionID),
	)
	return tgbotapi.NewInlineKeyboardMarkup(row)
}
