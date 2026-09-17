package bot

import (
	"strings"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// SplitMessage splits a long message text into chunks of at most chunkSize runes,
// preferring to break at newlines or spaces.
func SplitMessage(text string, chunkSize int) []string {
	if len(text) <= chunkSize {
		return []string{text}
	}
	var chunks []string
	runes := []rune(text)
	for len(runes) > 0 {
		if len(runes) <= chunkSize {
			chunks = append(chunks, string(runes))
			break
		}
		cutPoint := chunkSize
		// Search backwards for a newline
		for i := chunkSize; i > chunkSize-300 && i > 0; i-- {
			if runes[i] == '\n' {
				cutPoint = i + 1
				break
			}
		}
		// If no newline, search for a space
		if cutPoint == chunkSize {
			for i := chunkSize; i > chunkSize-150 && i > 0; i-- {
				if runes[i] == ' ' {
					cutPoint = i + 1
					break
				}
			}
		}
		chunks = append(chunks, string(runes[:cutPoint]))
		runes = runes[cutPoint:]
	}
	return chunks
}

// SendSafeMessage attempts to send a message using Markdown. If Telegram returns
// a markdown formatting parse error, it automatically falls back to sending plain text.
func SendSafeMessage(bot *tgbotapi.BotAPI, chatID int64, text string, replyMarkup interface{}) (tgbotapi.Message, error) {
	msg := tgbotapi.NewMessage(chatID, text)
	msg.ParseMode = tgbotapi.ModeMarkdown
	if replyMarkup != nil {
		msg.ReplyMarkup = replyMarkup
	}

	sent, err := bot.Send(msg)
	if err != nil {
		// Fallback to plain text without parse mode
		msg.ParseMode = ""
		return bot.Send(msg)
	}
	return sent, nil
}

// EditSafeMessage attempts to edit a message text using Markdown. If it fails, falls back to plain text.
func EditSafeMessage(bot *tgbotapi.BotAPI, chatID int64, messageID int, text string, replyMarkup *tgbotapi.InlineKeyboardMarkup) (tgbotapi.Message, error) {
	edit := tgbotapi.NewEditMessageText(chatID, messageID, text)
	edit.ParseMode = tgbotapi.ModeMarkdown
	if replyMarkup != nil {
		edit.ReplyMarkup = replyMarkup
	}

	sent, err := bot.Send(edit)
	if err != nil {
		if strings.Contains(err.Error(), "message is not modified") {
			return sent, nil
		}
		// Fallback to plain text
		edit.ParseMode = ""
		return bot.Send(edit)
	}
	return sent, nil
}
