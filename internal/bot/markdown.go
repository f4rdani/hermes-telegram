package bot

import (
	"html"
	"regexp"
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

var (
	reFencedCode = regexp.MustCompile("(?s)```(\\w*)\n?(.*?)```")
	reInlineCode = regexp.MustCompile("`([^`\n]+)`")
	reHeader     = regexp.MustCompile(`(?m)^(#{1,6})\s+(.*)`)
	reBold       = regexp.MustCompile(`\*\*(.+?)\*\*`)
	reItalicUS   = regexp.MustCompile(`__(.+?)__`)
	reItalicAst  = regexp.MustCompile(`\*([^\*\n]+)\*`)
	reStrike     = regexp.MustCompile(`~~(.+?)~~`)
)

// MarkdownToTelegramHTML converts standard Markdown (GFM) to Telegram-safe HTML.
// Telegram's HTML parser is much more forgiving than MarkdownV2 and doesn't require
// escaping normal punctuation like dots, dashes, or exclamation marks.
func MarkdownToTelegramHTML(text string) string {
	// 1. Escape existing HTML entities so they don't break our tags
	text = html.EscapeString(text)

	// 2. Extract and protect fenced code blocks
	var codeBlocks []string
	text = reFencedCode.ReplaceAllStringFunc(text, func(match string) string {
		parts := reFencedCode.FindStringSubmatch(match)
		lang := ""
		code := ""
		if len(parts) == 3 {
			lang = parts[1]
			code = parts[2]
		}
		tag := "<pre>"
		if lang != "" {
			tag = "<pre><code class=\"language-" + lang + "\">"
		}
		endTag := "</pre>"
		if lang != "" {
			endTag = "</code></pre>"
		}
		placeholder := "<<CB_" + string(rune('A'+len(codeBlocks))) + ">>"
		codeBlocks = append(codeBlocks, tag+strings.Trim(code, "\n")+endTag)
		return placeholder
	})

	// 3. Extract and protect inline codes
	var inlineCodes []string
	text = reInlineCode.ReplaceAllStringFunc(text, func(match string) string {
		parts := reInlineCode.FindStringSubmatch(match)
		code := parts[1]
		placeholder := "<<IC_" + string(rune('A'+len(inlineCodes))) + ">>"
		inlineCodes = append(inlineCodes, "<code>"+code+"</code>")
		return placeholder
	})

	// 4. Convert Headers (#, ##, ###) to <b>Header</b>\n
	text = reHeader.ReplaceAllString(text, "<b>$2</b>")

	// 5. Convert **bold** to <b>bold</b>
	text = reBold.ReplaceAllString(text, "<b>$1</b>")

	// 6. Convert __italic__ to <i>italic</i>
	text = reItalicUS.ReplaceAllString(text, "<i>$1</i>")

	// 7. Convert *italic* to <i>italic</i> (only if not inside a word)
	text = reItalicAst.ReplaceAllString(text, "<i>$1</i>")

	// 8. Convert ~~strikethrough~~ to <s>strikethrough</s>
	text = reStrike.ReplaceAllString(text, "<s>$1</s>")

	// 9. Convert [text](url) to <a href="url">text</a>
	reLink := regexp.MustCompile(`\[([^\]]+)\]\(([^)]+)\)`)
	text = reLink.ReplaceAllString(text, `<a href="$2">$1</a>`)

	// 10. Restore protected code blocks and inline codes
	for i, cb := range codeBlocks {
		placeholder := "<<CB_" + string(rune('A'+i)) + ">>"
		text = strings.Replace(text, placeholder, cb, 1)
	}
	for i, ic := range inlineCodes {
		placeholder := "<<IC_" + string(rune('A'+i)) + ">>"
		text = strings.Replace(text, placeholder, ic, 1)
	}

	return text
}

// SendSafeMessage attempts to send a message using HTML formatting converted from Markdown.
// If Telegram returns a formatting parse error, it automatically falls back to plain text.
func SendSafeMessage(bot *tgbotapi.BotAPI, chatID int64, text string, replyMarkup interface{}) (tgbotapi.Message, error) {
	htmlText := MarkdownToTelegramHTML(text)
	msg := tgbotapi.NewMessage(chatID, htmlText)
	msg.ParseMode = tgbotapi.ModeHTML
	if replyMarkup != nil {
		msg.ReplyMarkup = replyMarkup
	}

	sent, err := bot.Send(msg)
	if err != nil {
		// Fallback to plain text without parse mode if HTML still fails
		msg.Text = text
		msg.ParseMode = ""
		return bot.Send(msg)
	}
	return sent, nil
}

// EditSafeMessage attempts to edit a message text using HTML formatting. If it fails, falls back to plain text.
func EditSafeMessage(bot *tgbotapi.BotAPI, chatID int64, messageID int, text string, replyMarkup *tgbotapi.InlineKeyboardMarkup) (tgbotapi.Message, error) {
	htmlText := MarkdownToTelegramHTML(text)
	edit := tgbotapi.NewEditMessageText(chatID, messageID, htmlText)
	edit.ParseMode = tgbotapi.ModeHTML
	if replyMarkup != nil {
		edit.ReplyMarkup = replyMarkup
	}

	sent, err := bot.Send(edit)
	if err != nil {
		if strings.Contains(err.Error(), "message is not modified") {
			return sent, nil
		}
		// Fallback to plain text
		edit.Text = text
		edit.ParseMode = ""
		return bot.Send(edit)
	}
	return sent, nil
}
