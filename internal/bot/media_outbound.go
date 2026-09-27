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

	// Fallback: bare absolute paths to video files without any tag, e.g. "/root/video.mp4"
	// Catches cases where the agent forgets VIDEO: prefix or markdown wrapping.
	plainVideoPathRegex = regexp.MustCompile(`(?m)(^|\s)(/[^\s` + "`" + `\"'\]\)]+?\.(?:mp4|mov|mkv|webm))`)
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
	// NOTE: video files are sometimes emitted as ![title](/path.mp4) — treat them as video, not photo.
	imgMatches := markdownImageRegex.FindAllStringSubmatch(text, -1)
	for _, m := range imgMatches {
		if len(m) > 2 {
			alt := strings.TrimSpace(m[1])
			p := strings.TrimSpace(m[2])
			if !seenPaths[p] && fileExists(p) {
				seenPaths[p] = true
				isVid := isVideoFile(p)
				mediaList = append(mediaList, ExtractedMedia{
					FilePath: p,
					IsImage:  !isVid && isImageFile(p),
					IsVideo:  isVid,
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
				isVid := isVideoFile(p)
				mediaList = append(mediaList, ExtractedMedia{
					FilePath: p,
					IsImage:  !isVid && isImageFile(p),
					IsVideo:  isVid,
					Caption:  title,
				})
			}
		}
	}

	// 3b. Fallback: bare absolute video paths without VIDEO: tag or markdown.
	// Ensures "kadang file kadang video" never happens just because the agent forgot the prefix.
	plainMatches := plainVideoPathRegex.FindAllStringSubmatch(text, -1)
	for _, m := range plainMatches {
		if len(m) > 2 {
			p := strings.TrimSpace(m[2])
			if !seenPaths[p] && fileExists(p) && isVideoFile(p) {
				seenPaths[p] = true
				mediaList = append(mediaList, ExtractedMedia{
					FilePath: p,
					IsImage:  false,
					IsVideo:  true,
				})
			}
		}
	}

	// 4. Send all extracted files/images/videos directly to Telegram
	// Policy: try native playable video first, fallback to document on failure (e.g. >50MB Bot API limit).
	// No auto-compress per user request — oversized videos arrive as files with a clear label.
	for _, item := range mediaList {
		switch {
		case item.IsVideo:
			// VIDEO = native playable video bubble (streams inline, no download needed)
			vid := tgbotapi.NewVideo(chatID, tgbotapi.FilePath(item.FilePath))
			vid.SupportsStreaming = true
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
					log.Printf("[bot] Successfully sent document (video fallback) %s (msg_id: %d)", item.FilePath, sentDoc.MessageID)
					s.sessMgr.AddTelegramMsgID(userID, sentDoc.MessageID)
				} else {
					log.Printf("[bot] Failed to send outbound document fallback (%s): %v", item.FilePath, errDoc)
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
			// Also collapse markdown image/link wrappers for videos to the same notice.
			reImg := regexp.MustCompile(`!\[[^\]]*\]\(` + regexp.QuoteMeta(p) + `\)`)
			cleanText = reImg.ReplaceAllString(cleanText, fmt.Sprintf("🎬 _[Video terkirim: `%s`]", base))
			reLink := regexp.MustCompile(`\[[^\]]+\]\(` + regexp.QuoteMeta(p) + `\)`)
			cleanText = reLink.ReplaceAllString(cleanText, fmt.Sprintf("🎬 _[Video terkirim: `%s`]", base))
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
