package bot

import (
	"encoding/json"
	"fmt"
	"html"
	"log"
	"regexp"
	"strconv"
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
	reFencedCode   = regexp.MustCompile("(?s)```(\\w*)\n?(.*?)```")
	reInlineCode   = regexp.MustCompile("`([^`\n]+)`")
	reHeader       = regexp.MustCompile(`(?m)^(#{1,6})\s+(.*)`)
	reBold         = regexp.MustCompile(`\*\*(.+?)\*\*`)
	reItalicUS     = regexp.MustCompile(`__(.+?)__`)
	reItalicAst    = regexp.MustCompile(`\*([^\*\n]+)\*`)
	reStrike       = regexp.MustCompile(`~~(.+?)~~`)
	reDashLine     = regexp.MustCompile(`^-{8,}$`)
	reMultiSpace   = regexp.MustCompile(`\s{2,}`)
	reStartsNum    = regexp.MustCompile(`^\d+\s`)
)

// ConvertASCIITablesToGFM detects ASCII text tables framed by dashed lines (---)
// and converts them to standard GitHub Flavored Markdown (GFM) tables (| col1 | col2 |).
// Telegram 12.9 natively renders GFM tables as beautiful visual UI tables.
func ConvertASCIITablesToGFM(s string) string {
	lines := strings.Split(s, "\n")
	var out []string
	i := 0
	n := len(lines)

	for i < n {
		line := lines[i]
		trimmed := strings.TrimSpace(line)

		hasTitle := false
		title := ""
		dashIdx := -1

		if reDashLine.MatchString(trimmed) {
			dashIdx = i
		} else if i+1 < n && reDashLine.MatchString(strings.TrimSpace(lines[i+1])) {
			hasTitle = true
			title = trimmed
			dashIdx = i + 1
		}

		if dashIdx != -1 && dashIdx+2 < n && reDashLine.MatchString(strings.TrimSpace(lines[dashIdx+2])) {
			headerLine := strings.TrimSpace(lines[dashIdx+1])
			rawHeaders := reMultiSpace.Split(headerLine, -1)
			var headers []string
			for _, h := range rawHeaders {
				if th := strings.TrimSpace(h); th != "" {
					headers = append(headers, th)
				}
			}
			numCols := len(headers)

			if numCols >= 2 {
				i = dashIdx + 3
				var rows [][]string

				for i < n && !reDashLine.MatchString(strings.TrimSpace(lines[i])) {
					rowRaw := lines[i]
					rowTrimmed := strings.TrimSpace(rowRaw)
					if rowTrimmed != "" {
						isContinuation := false
						if len(rows) > 0 {
							firstHeader := strings.ToLower(headers[0])
							if strings.HasPrefix(rowRaw, "   ") || ((firstHeader == "no" || firstHeader == "id" || firstHeader == "#") && !reStartsNum.MatchString(rowTrimmed)) {
								isContinuation = true
							}
						}

						if isContinuation && len(rows) > 0 {
							lastRow := rows[len(rows)-1]
							lastRow[len(lastRow)-1] += " " + rowTrimmed
						} else {
							rawParts := reMultiSpace.Split(rowTrimmed, -1)
							var parts []string
							for _, p := range rawParts {
								if tp := strings.TrimSpace(p); tp != "" {
									parts = append(parts, tp)
								}
							}
							if len(parts) > numCols {
								mergedLast := strings.Join(parts[numCols-1:], " ")
								parts = append(parts[:numCols-1], mergedLast)
							}
							for len(parts) < numCols {
								parts = append(parts, "")
							}
							rows = append(rows, parts)
						}
					}
					i++
				}

				if i < n && reDashLine.MatchString(strings.TrimSpace(lines[i])) {
					i++
				}

				if hasTitle && title != "" {
					out = append(out, "### "+title+"\n")
				}
				out = append(out, "| "+strings.Join(headers, " | ")+" |")
				var seps []string
				for k := 0; k < numCols; k++ {
					seps = append(seps, "---")
				}
				out = append(out, "| "+strings.Join(seps, " | ")+" |")
				for _, r := range rows {
					out = append(out, "| "+strings.Join(r, " | ")+" |")
				}
				out = append(out, "")
				continue
			}
		}

		out = append(out, line)
		i++
	}

	return strings.Join(out, "\n")
}

// MarkdownToTelegramHTML converts standard Markdown (GFM) to Telegram-safe HTML for legacy clients.
func MarkdownToTelegramHTML(text string) string {
	// 1. Extract and protect fenced code blocks
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
		codeBlocks = append(codeBlocks, tag+html.EscapeString(strings.Trim(code, "\n"))+endTag)
		return placeholder
	})

	// 2. Extract and protect inline codes
	var inlineCodes []string
	text = reInlineCode.ReplaceAllStringFunc(text, func(match string) string {
		parts := reInlineCode.FindStringSubmatch(match)
		code := parts[1]
		placeholder := "<<IC_" + string(rune('A'+len(inlineCodes))) + ">>"
		inlineCodes = append(inlineCodes, "<code>"+html.EscapeString(code)+"</code>")
		return placeholder
	})

	// 3. Escape remaining HTML entities
	text = html.EscapeString(text)

	// 4. Convert Headers (#, ##, ###) to <b>Header</b>\n
	text = reHeader.ReplaceAllString(text, "<b>$2</b>")

	// 5. Convert **bold** to <b>bold</b>
	text = reBold.ReplaceAllString(text, "<b>$1</b>")

	// 6. Convert __italic__ to <i>italic</i>
	text = reItalicUS.ReplaceAllString(text, "<i>$1</i>")

	// 7. Convert *italic* to <i>italic</i>
	text = reItalicAst.ReplaceAllString(text, "<i>$1</i>")

	// 8. Convert ~~strikethrough~~ to <s>strikethrough</s>
	text = reStrike.ReplaceAllString(text, "<s>$1</s>")

	// 9. Convert [text](url) to <a href="$2">$1</a>
	reLink := regexp.MustCompile(`\[([^\]]+)\]\(([^)]+)\)`)
	text = reLink.ReplaceAllString(text, `<a href="$2">$1</a>`)

	// 10. Restore protected blocks
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

// SendRichMessage sends a rich message using Telegram 12.9+ Bot API 10.x sendRichMessage.
// Supports native visual tables, headings, LaTeX math, blockquotes, checklists, up to 32,768 chars.
func SendRichMessage(bot *tgbotapi.BotAPI, chatID int64, markdown string, replyMarkup interface{}) (tgbotapi.Message, error) {
	// Auto-convert any legacy plain ASCII dash tables into native GFM tables so Telegram renders them visually
	markdown = ConvertASCIITablesToGFM(markdown)

	richJSON, err := json.Marshal(map[string]interface{}{
		"markdown": markdown,
	})
	if err != nil {
		return tgbotapi.Message{}, err
	}

	params := tgbotapi.Params{
		"chat_id":      strconv.FormatInt(chatID, 10),
		"rich_message": string(richJSON),
	}
	_ = params.AddInterface("reply_markup", replyMarkup)

	apiResp, err := bot.MakeRequest("sendRichMessage", params)
	if err != nil {
		return tgbotapi.Message{}, err
	}

	var msg tgbotapi.Message
	if err := json.Unmarshal(apiResp.Result, &msg); err != nil {
		return tgbotapi.Message{}, fmt.Errorf("failed to unmarshal sendRichMessage result: %w", err)
	}
	return msg, nil
}

// EditRichMessage edits an existing message using Telegram 12.9+ Bot API 10.x editMessageText with rich_message.
func EditRichMessage(bot *tgbotapi.BotAPI, chatID int64, messageID int, markdown string, replyMarkup interface{}) (tgbotapi.Message, error) {
	// Auto-convert any legacy plain ASCII dash tables into native GFM tables so Telegram renders them visually
	markdown = ConvertASCIITablesToGFM(markdown)

	richJSON, err := json.Marshal(map[string]interface{}{
		"markdown": markdown,
	})
	if err != nil {
		return tgbotapi.Message{}, err
	}

	params := tgbotapi.Params{
		"chat_id":      strconv.FormatInt(chatID, 10),
		"message_id":   strconv.Itoa(messageID),
		"rich_message": string(richJSON),
	}
	_ = params.AddInterface("reply_markup", replyMarkup)

	apiResp, err := bot.MakeRequest("editMessageText", params)
	if err != nil {
		return tgbotapi.Message{}, err
	}

	var msg tgbotapi.Message
	if err := json.Unmarshal(apiResp.Result, &msg); err != nil {
		return tgbotapi.Message{MessageID: messageID}, nil
	}
	return msg, nil
}

// SendSafeMessage attempts to send a message using modern Telegram 12.9+ Rich Message (native tables, headings).
// If rich message fails, it seamlessly falls back to legacy HTML, and finally plain text.
func SendSafeMessage(bot *tgbotapi.BotAPI, chatID int64, text string, replyMarkup interface{}) (tgbotapi.Message, error) {
	// 1. Try modern Telegram 12.9 Rich Message first
	msg, err := SendRichMessage(bot, chatID, text, replyMarkup)
	if err == nil {
		return msg, nil
	}

	log.Printf("[bot] SendRichMessage failed (%v), falling back to legacy HTML", err)

	// 2. Legacy fallback: split if text > 4000 runes
	chunks := SplitMessage(text, 4000)
	var lastSent tgbotapi.Message
	for i, chunk := range chunks {
		htmlText := MarkdownToTelegramHTML(chunk)
		m := tgbotapi.NewMessage(chatID, htmlText)
		m.ParseMode = tgbotapi.ModeHTML
		if i == len(chunks)-1 && replyMarkup != nil {
			m.ReplyMarkup = replyMarkup
		}
		sent, err := bot.Send(m)
		if err != nil {
			// Fallback to plain text
			m.Text = chunk
			m.ParseMode = ""
			sent, err = bot.Send(m)
			if err != nil {
				return lastSent, err
			}
		}
		lastSent = sent
	}
	return lastSent, nil
}

// EditSafeMessage attempts to edit a message using Rich Message first, falling back to legacy HTML or plain text.
func EditSafeMessage(bot *tgbotapi.BotAPI, chatID int64, messageID int, text string, replyMarkup *tgbotapi.InlineKeyboardMarkup) (tgbotapi.Message, error) {
	// 1. Try modern Telegram 12.9 Rich Message first
	msg, err := EditRichMessage(bot, chatID, messageID, text, replyMarkup)
	if err == nil {
		return msg, nil
	}
	if strings.Contains(err.Error(), "message is not modified") {
		return tgbotapi.Message{MessageID: messageID}, nil
	}

	log.Printf("[bot] EditRichMessage failed (%v), falling back to legacy HTML", err)

	legacyText := text
	if len([]rune(legacyText)) > 4000 {
		chunks := SplitMessage(legacyText, 4000)
		if len(chunks) > 0 {
			legacyText = chunks[0]
		}
	}

	htmlText := MarkdownToTelegramHTML(legacyText)
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
		edit.Text = legacyText
		edit.ParseMode = ""
		return bot.Send(edit)
	}
	return sent, nil
}
