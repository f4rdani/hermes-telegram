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
	// Matches VIDEO:<path>, MEDIA:<path>, FILE:<path>, DOCUMENT:<path>
	mediaTagRegex = regexp.MustCompile(`(?i)(?:VIDEO|MEDIA|FILE|DOCUMENT):\s*[` + "`" + `\"'\"]?([/\w\.-]+)[` + "`" + `\"'\"]?`)

	// Matches markdown image syntax: ![alt](/path/to/image.png)
	markdownImageRegex = regexp.MustCompile(`!\[([^\]]*)\]\(([/\w\.-]+)\)`)

	// Matches markdown file link syntax: [title](/path/to/file.ext)
	markdownLinkRegex = regexp.MustCompile(`\[([^\]]+)\]\(([/\w\.-]+\.(?:txt|pdf|zip|tar\.gz|gz|csv|json|py|go|sh|html|css|md|png|jpg|jpeg|webp|gif|mp4|mov|mkv|webm|mp3|m4a|ogg|wav))\)`)
)

type ExtractedMedia struct {
	FilePath string
	IsImage  bool
	IsVideo  bool
	Caption  string
}

// ProcessOutboundMedia scans the agent's text response for media/file paths, sends them to Telegram,
// and returns the sanitized text response with media tags replaced by user-friendly notices.
func (s *BotServer) ProcessOutboundMedia(chatID, userID int64, text string) string {
	seenPaths := make(map[string]bool)
	var mediaList []ExtractedMedia

	// 1. Scan for explicit tags: VIDEO:<path>, MEDIA:<path>, FILE:<path>, DOCUMENT:<path>
	tagMatches := mediaTagRegex.FindAllStringSubmatch(text, -1)
	for _, m := range tagMatches {
		if len(m) > 1 {
			p := strings.TrimSpace(m[1])
			if !seenPaths[p] && fileExists(p) {
				seenPaths[p] = true
				mediaList = append(mediaList, ExtractedMedia{
					FilePath: p,
					IsImage:  isImageFile(p),
					IsVideo:  isVideoFile(p),
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
					IsVideo:  isVideoFile(p),
					Caption:  title,
				})
			}
		}
	}

	// 4. Send all extracted files/images/videos directly to Telegram
	for _, item := range mediaList {
		switch {
		case item.IsVideo:
			// VIDEO = native playable video bubble (streams inline, no download needed)
			vid := tgbotapi.NewVideo(chatID, tgbotapi.FilePath(item.FilePath))
			if item.Caption != "" {
				vid.Caption = item.Caption
			}
			sent, err := s.bot.Send(vid)
			if err != nil {
				log.Printf("[bot] Failed to send outbound video (%s), falling back to document: %v", item.FilePath, err)
				doc := tgbotapi.NewDocument(chatID, tgbotapi.FilePath(item.FilePath))
				if item.Caption != "" {
					doc.Caption = item.Caption
				}
				if sentDoc, errDoc := s.bot.Send(doc); errDoc == nil {
					s.sessMgr.AddTelegramMsgID(userID, sentDoc.MessageID)
				}
			} else {
				log.Printf("[bot] Successfully sent video %s (msg_id: %d)", item.FilePath, sent.MessageID)
				s.sessMgr.AddTelegramMsgID(userID, sent.MessageID)
			}
		case item.IsImage:
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
		default:
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
		switch {
		case isVideoFile(p):
			re := regexp.MustCompile(`(?m)^.*(?:VIDEO|MEDIA|FILE|DOCUMENT):\s*[` + "`" + `\"'\"]?` + regexp.QuoteMeta(p) + `[` + "`" + `\"'\"]?.*$\n?`)
			cleanText = re.ReplaceAllString(cleanText, fmt.Sprintf("🎬 _[Video terkirim: `%s`]\n", base))
		case isImageFile(p):
			re := regexp.MustCompile(`(?m)^.*(?:VIDEO|MEDIA|FILE|DOCUMENT):\s*[` + "`" + `\"'\"]?` + regexp.QuoteMeta(p) + `[` + "`" + `\"'\"]?.*$\n?`)
			cleanText = re.ReplaceAllString(cleanText, fmt.Sprintf("🖼️ _[Foto / Screenshot terkirim: `%s`]\n", base))
		default:
			re := regexp.MustCompile(`(?m)^.*(?:VIDEO|MEDIA|FILE|DOCUMENT):\s*[` + "`" + `\"'\"]?` + regexp.QuoteMeta(p) + `[` + "`" + `\"'\"]?.*$\n?`)
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

func isVideoFile(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".mp4", ".mov", ".mkv", ".webm":
		return true
	default:
		return false
	}
}
