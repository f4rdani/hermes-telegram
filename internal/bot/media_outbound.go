package bot

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

var (
	// Matches MEDIA:<path>, FILE:<path>, DOCUMENT:<path>
	mediaTagRegex = regexp.MustCompile(`(?i)(?:MEDIA|FILE|DOCUMENT):\s*[` + "`" + `"'"]?([/\w\.-]+)[` + "`" + `"'"]?`)

	// Matches markdown image syntax: ![alt](/path/to/image.png)
	markdownImageRegex = regexp.MustCompile(`!\[([^\]]*)\]\(([/\w\.-]+)\)`)

	// Matches markdown file link syntax: [title](/path/to/file.ext)
	markdownLinkRegex = regexp.MustCompile(`\[([^\]]+)\]\(([/\w\.-]+\.(?:txt|pdf|zip|tar\.gz|gz|csv|json|py|go|sh|html|css|md|png|jpg|jpeg|webp|gif))\)`)
)

type ExtractedMedia struct {
	FilePath string
	IsImage  bool
	Caption  string
}

// ProcessOutboundMedia scans the agent's text response for media/file paths, sends them to Telegram,
// and returns the sanitized text response with media tags replaced by user-friendly notices.
func (s *BotServer) ProcessOutboundMedia(chatID, userID int64, text string) string {
	seenPaths := make(map[string]bool)
	var mediaList []ExtractedMedia

	// 1. Scan for explicit tags: MEDIA:<path>, FILE:<path>, DOCUMENT:<path>
	tagMatches := mediaTagRegex.FindAllStringSubmatch(text, -1)
	for _, m := range tagMatches {
		if len(m) > 1 {
			p := strings.TrimSpace(m[1])
			if !seenPaths[p] && fileExists(p) {
				seenPaths[p] = true
				mediaList = append(mediaList, ExtractedMedia{
					FilePath: p,
					IsImage:  isImageFile(p),
				})
			}
		}
	}

	// 2. Scan for markdown images: ![alt](/path)
	imgMatches := markdownImageRegex.FindAllStringSubmatch(text, -1)
	for _, m := range imgMatches {
		if len(m) > 2 {
			alt := strings.TrimSpace(m[1])
			p := strings.TrimSpace(m[2])
			if !seenPaths[p] && fileExists(p) {
				seenPaths[p] = true
				mediaList = append(mediaList, ExtractedMedia{
					FilePath: p,
					IsImage:  true,
					Caption:  alt,
				})
			}
		}
	}

	// 3. Scan for markdown links to local files: [name](/path)
	linkMatches := markdownLinkRegex.FindAllStringSubmatch(text, -1)
	for _, m := range linkMatches {
		if len(m) > 2 {
			title := strings.TrimSpace(m[1])
			p := strings.TrimSpace(m[2])
			if !seenPaths[p] && fileExists(p) {
				seenPaths[p] = true
				mediaList = append(mediaList, ExtractedMedia{
					FilePath: p,
					IsImage:  isImageFile(p),
					Caption:  title,
				})
			}
		}
	}

	// 4. Send all extracted files/images directly to Telegram
	for _, item := range mediaList {
		if item.IsImage {
			photo := tgbotapi.NewPhoto(chatID, tgbotapi.FilePath(item.FilePath))
			if item.Caption != "" {
				photo.Caption = item.Caption
			}
			sent, err := s.bot.Send(photo)
			if err != nil {
				log.Printf("[bot] Failed to send outbound photo (%s): %v", item.FilePath, err)
			} else {
				log.Printf("[bot] Successfully sent photo %s (msg_id: %d)", item.FilePath, sent.MessageID)
				s.sessMgr.AddTelegramMsgID(userID, sent.MessageID)
			}
		} else {
			doc := tgbotapi.NewDocument(chatID, tgbotapi.FilePath(item.FilePath))
			if item.Caption != "" {
				doc.Caption = item.Caption
			}
			sent, err := s.bot.Send(doc)
			if err != nil {
				log.Printf("[bot] Failed to send outbound document (%s): %v", item.FilePath, err)
			} else {
				log.Printf("[bot] Successfully sent document %s (msg_id: %d)", item.FilePath, sent.MessageID)
				s.sessMgr.AddTelegramMsgID(userID, sent.MessageID)
			}
		}
	}

	// 5. Replace tags in the final text for clean, professional presentation
	cleanText := text
	for p := range seenPaths {
		base := filepath.Base(p)
		if isImageFile(p) {
			re := regexp.MustCompile(`(?m)^.*(?:MEDIA|FILE|DOCUMENT):\s*[` + "`" + `"'"]?` + regexp.QuoteMeta(p) + `[` + "`" + `"'"]?.*$\n?`)
			cleanText = re.ReplaceAllString(cleanText, fmt.Sprintf("🖼️ _[Foto / Screenshot terkirim: `%s`]\n", base))
		} else {
			re := regexp.MustCompile(`(?m)^.*(?:MEDIA|FILE|DOCUMENT):\s*[` + "`" + `"'"]?` + regexp.QuoteMeta(p) + `[` + "`" + `"'"]?.*$\n?`)
			cleanText = re.ReplaceAllString(cleanText, fmt.Sprintf("📎 _[File dokumen terkirim: `%s`]\n", base))
		}
	}

	return cleanText
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return !info.IsDir()
}

func isImageFile(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".png", ".jpg", ".jpeg", ".webp", ".gif", ".bmp":
		return true
	default:
		return false
	}
}
