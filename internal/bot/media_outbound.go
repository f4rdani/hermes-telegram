package bot

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

var (
	// Matches explicit tags: VIDEO:<path>, MEDIA:<path>, FILE:<path>, DOCUMENT:<path>, IMAGE:<path>, PHOTO:<path>, SCREENSHOT:<path>, AUDIO:<path>
	mediaTagRegex = regexp.MustCompile(`(?i)(?:VIDEO|MEDIA|FILE|DOCUMENT|IMAGE|PHOTO|SCREENSHOT|AUDIO):\s*(?:[` + "`" + `\"']([^` + "`" + `\"'\n]+)[` + "`" + `\"']|([/\w\.\-]+))`)

	// Matches markdown image syntax: ![alt](/path/to/image.png)
	markdownImageRegex = regexp.MustCompile(`!\[([^\]]*)\]\(([/\w\.\-]+)\)`)

	// Matches markdown file link syntax: [title](/path/to/file.ext) - excludes code, scripts, and sensitive extensions
	markdownLinkRegex = regexp.MustCompile(`\[([^\]]+)\]\(([/\w\.\-]+\.(?:pdf|zip|tar\.gz|gz|csv|png|jpg|jpeg|webp|gif|bmp|svg|mp4|mov|mkv|webm|mp3|m4a|ogg|wav|docx|xlsx|pptx|ass|srt))\)`)

	// Fallback: bare absolute file paths without any tag or markdown syntax.
	// Only matches real media formats (images, videos, audio, PDF, ZIP, subtitles).
	// NEVER matches code, scripts, configs, databases, or logs!
	plainFilePathRegex = regexp.MustCompile(`(?i)(?:^|[\s\"'\(\[])(/[\w\.\-]+(?:/[\w\.\-]+)*\.(?:png|jpg|jpeg|webp|gif|bmp|svg|mp4|mov|mkv|webm|mp3|m4a|ogg|wav|pdf|zip|tar\.gz|gz|docx|xlsx|pptx|ass|srt))\b`)

	blockedOutboundExtensions = map[string]bool{
		".sh":      true,
		".bash":    true,
		".zsh":     true,
		".py":      true,
		".go":      true,
		".js":      true,
		".ts":      true,
		".jsx":     true,
		".tsx":     true,
		".env":     true,
		".db":      true,
		".sqlite":  true,
		".sqlite3": true,
		".key":     true,
		".pem":     true,
		".crt":     true,
		".json":    true,
		".yaml":    true,
		".yml":     true,
		".toml":    true,
		".ini":     true,
		".conf":    true,
		".service": true,
		".bak":     true,
		".old":     true,
		".log":     true,
		".html":    true,
		".css":     true,
		".c":       true,
		".cpp":     true,
		".h":       true,
		".rs":      true,
		".php":     true,
	}
)

func isBlockedDirectory(path string) bool {
	clean := filepath.Clean(path)
	blockedPrefixes := []string{
		"/root/scripts",
		"/root/.ssh",
		"/root/.hermes",
		"/root/.gemini",
		"/root/backups",
		"/root/apps",
		"/etc",
		"/var",
		"/proc",
		"/sys",
		"/usr",
		"/bin",
		"/sbin",
		"/lib",
		"/lib64",
	}
	for _, prefix := range blockedPrefixes {
		if clean == prefix || strings.HasPrefix(clean, prefix+"/") {
			return true
		}
	}
	return false
}

func isBlockedFile(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	if blockedOutboundExtensions[ext] {
		return true
	}
	if isBlockedDirectory(path) {
		return true
	}
	return false
}

func stripCodeBlocks(text string) string {
	stripped := reFencedCode.ReplaceAllString(text, " ")
	return reInlineCode.ReplaceAllString(stripped, " ")
}

type ExtractedMedia struct {
	FilePath string
	IsImage  bool
	IsVideo  bool
	IsAudio  bool
	Caption  string
}

// ProcessOutboundMedia scans the agent's text response for media/file paths, sends them to Telegram,
// and returns the sanitized text response with media tags replaced by user-friendly notices.
// targetIDs optionally accepts [threadID, replyToMsgID].
func (s *BotServer) ProcessOutboundMedia(chatID, userID int64, text string, targetIDs ...int) string {
	var threadID int
	var targetReplyID int
	if len(targetIDs) > 0 && targetIDs[0] > 0 {
		threadID = targetIDs[0]
	}
	if len(targetIDs) > 1 && targetIDs[1] > 0 {
		targetReplyID = targetIDs[1]
	}

	seenPaths := make(map[string]bool)
	var mediaList []ExtractedMedia

	// Helper to resolve and check file existence and safety
	resolvePath := func(p string) (string, bool) {
		p = strings.TrimSpace(p)
		if p == "" {
			return "", false
		}
		// Security check: Never upload sensitive files or directories
		if isBlockedFile(p) {
			log.Printf("[bot] Blocked outbound upload of sensitive/system file: %s", p)
			return "", false
		}
		if fileExists(p) {
			return p, true
		}
		if !filepath.IsAbs(p) && s.cfg != nil && s.cfg.Hermes.WorkingDir != "" {
			joined := filepath.Join(s.cfg.Hermes.WorkingDir, p)
			if !isBlockedFile(joined) && fileExists(joined) {
				return joined, true
			}
		}
		return "", false
	}

	// 1. Scan for explicit tags: VIDEO:<path>, MEDIA:<path>, FILE:<path>, DOCUMENT:<path>, etc.
	tagMatches := mediaTagRegex.FindAllStringSubmatch(text, -1)
	for _, m := range tagMatches {
		p := strings.TrimSpace(m[1])
		if p == "" && len(m) > 2 {
			p = strings.TrimSpace(m[2])
		}
		if resolved, ok := resolvePath(p); ok && !seenPaths[resolved] {
			seenPaths[resolved] = true
			mediaList = append(mediaList, ExtractedMedia{
				FilePath: resolved,
				IsImage:  isImageFile(resolved),
				IsVideo:  isVideoFile(resolved),
				IsAudio:  isAudioFile(resolved),
			})
		}
	}

	// 2. Scan for markdown images: ![alt](/path)
	imgMatches := markdownImageRegex.FindAllStringSubmatch(text, -1)
	for _, m := range imgMatches {
		if len(m) > 2 {
			alt := strings.TrimSpace(m[1])
			if resolved, ok := resolvePath(m[2]); ok && !seenPaths[resolved] {
				seenPaths[resolved] = true
				isVid := isVideoFile(resolved)
				isAud := isAudioFile(resolved)
				mediaList = append(mediaList, ExtractedMedia{
					FilePath: resolved,
					IsImage:  !isVid && !isAud && isImageFile(resolved),
					IsVideo:  isVid,
					IsAudio:  isAud,
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
			if resolved, ok := resolvePath(m[2]); ok && !seenPaths[resolved] {
				seenPaths[resolved] = true
				isVid := isVideoFile(resolved)
				isAud := isAudioFile(resolved)
				mediaList = append(mediaList, ExtractedMedia{
					FilePath: resolved,
					IsImage:  !isVid && !isAud && isImageFile(resolved),
					IsVideo:  isVid,
					IsAudio:  isAud,
					Caption:  title,
				})
			}
		}
	}

	// 4. Fallback: bare absolute file paths without tags or markdown.
	// Only scan text OUTSIDE code blocks and inline code backticks!
	plainTextToScan := stripCodeBlocks(text)
	plainMatches := plainFilePathRegex.FindAllStringSubmatch(plainTextToScan, -1)
	for _, m := range plainMatches {
		if len(m) > 1 {
			if resolved, ok := resolvePath(m[1]); ok && !seenPaths[resolved] {
				seenPaths[resolved] = true
				mediaList = append(mediaList, ExtractedMedia{
					FilePath: resolved,
					IsImage:  isImageFile(resolved),
					IsVideo:  isVideoFile(resolved),
					IsAudio:  isAudioFile(resolved),
				})
			}
		}
	}

	// Send all extracted media directly to Telegram
	for _, item := range mediaList {
		sent, err := s.uploadMediaFile(chatID, threadID, targetReplyID, item)
		if err != nil {
			log.Printf("[bot] Failed to send outbound media (%s): %v", item.FilePath, err)
		} else if sent != nil && sent.MessageID > 0 {
			log.Printf("[bot] Successfully sent media %s (msg_id: %d, thread_id: %d)", item.FilePath, sent.MessageID, threadID)
			if s.sessMgr != nil {
				s.sessMgr.AddTelegramMsgID(userID, sent.MessageID)
			}
		}
	}

	// 5. Replace tags in the final text for clean, professional presentation
	cleanText := text
	for p := range seenPaths {
		base := filepath.Base(p)
		escaped := regexp.QuoteMeta(p)

		tagRe := regexp.MustCompile(`(?mi)^.*(?:VIDEO|MEDIA|FILE|DOCUMENT|IMAGE|PHOTO|SCREENSHOT|AUDIO):\s*[` + "`" + `\"']?` + escaped + `[` + "`" + `\"']?.*$\n?`)
		imgRe := regexp.MustCompile(`!\[[^\]]*\]\(` + escaped + `\)`)
		linkRe := regexp.MustCompile(`\[[^\]]+\]\(` + escaped + `\)`)
		loneRe := regexp.MustCompile(`(?m)^\s*[` + "`" + `\"']?` + escaped + `[` + "`" + `\"']?\s*$\n?`)

		switch {
		case isVideoFile(p):
			notice := fmt.Sprintf("🎬 _[Video terkirim: `%s`]", base)
			cleanText = tagRe.ReplaceAllString(cleanText, notice+"\n")
			cleanText = imgRe.ReplaceAllString(cleanText, notice)
			cleanText = linkRe.ReplaceAllString(cleanText, notice)
			cleanText = loneRe.ReplaceAllString(cleanText, notice+"\n")
		case isImageFile(p):
			notice := fmt.Sprintf("🖼️ _[Foto / Screenshot terkirim: `%s`]", base)
			cleanText = tagRe.ReplaceAllString(cleanText, notice+"\n")
			cleanText = imgRe.ReplaceAllString(cleanText, notice)
			cleanText = linkRe.ReplaceAllString(cleanText, notice)
			cleanText = loneRe.ReplaceAllString(cleanText, notice+"\n")
		case isAudioFile(p):
			notice := fmt.Sprintf("🎵 _[Audio terkirim: `%s`]", base)
			cleanText = tagRe.ReplaceAllString(cleanText, notice+"\n")
			cleanText = imgRe.ReplaceAllString(cleanText, notice)
			cleanText = linkRe.ReplaceAllString(cleanText, notice)
			cleanText = loneRe.ReplaceAllString(cleanText, notice+"\n")
		default:
			notice := fmt.Sprintf("📎 _[File dokumen terkirim: `%s`]", base)
			cleanText = tagRe.ReplaceAllString(cleanText, notice+"\n")
			cleanText = imgRe.ReplaceAllString(cleanText, notice)
			cleanText = linkRe.ReplaceAllString(cleanText, notice)
			cleanText = loneRe.ReplaceAllString(cleanText, notice+"\n")
		}
	}

	return cleanText
}

// uploadMediaFile uploads a single file to Telegram using native UploadFiles API.
// It supports topic routing via message_thread_id, streaming playback for video,
// and falls back gracefully to sendDocument if the media format or size exceeds limits.
func (s *BotServer) uploadMediaFile(chatID int64, threadID int, replyToMsgID int, item ExtractedMedia) (*tgbotapi.Message, error) {
	if s.bot == nil {
		return nil, fmt.Errorf("telegram bot instance is nil")
	}

	endpoint := "sendDocument"
	field := "document"
	switch {
	case item.IsImage:
		endpoint = "sendPhoto"
		field = "photo"
	case item.IsVideo:
		endpoint = "sendVideo"
		field = "video"
	case item.IsAudio:
		endpoint = "sendAudio"
		field = "audio"
	}

	params := tgbotapi.Params{
		"chat_id": strconv.FormatInt(chatID, 10),
	}
	if threadID > 0 {
		params["message_thread_id"] = strconv.Itoa(threadID)
	}
	if replyToMsgID > 0 {
		params["reply_to_message_id"] = strconv.Itoa(replyToMsgID)
		params["allow_sending_without_reply"] = "true"
	}
	if item.Caption != "" {
		params["caption"] = item.Caption
	}
	if item.IsVideo {
		params["supports_streaming"] = "true"
	}

	reqFile := tgbotapi.RequestFile{
		Name: field,
		Data: tgbotapi.FilePath(item.FilePath),
	}

	apiResp, err := s.bot.UploadFiles(endpoint, params, []tgbotapi.RequestFile{reqFile})
	if err != nil {
		// If photo, video, or audio fails (e.g. format rejection, dimension issue), fallback to sendDocument
		if endpoint != "sendDocument" {
			log.Printf("[bot] Failed to send %s (%s): %v, falling back to sendDocument", field, item.FilePath, err)
			docParams := tgbotapi.Params{
				"chat_id": strconv.FormatInt(chatID, 10),
			}
			if threadID > 0 {
				docParams["message_thread_id"] = strconv.Itoa(threadID)
			}
			if replyToMsgID > 0 {
				docParams["reply_to_message_id"] = strconv.Itoa(replyToMsgID)
				docParams["allow_sending_without_reply"] = "true"
			}
			if item.Caption != "" {
				docParams["caption"] = item.Caption
			}
			docFile := tgbotapi.RequestFile{
				Name: "document",
				Data: tgbotapi.FilePath(item.FilePath),
			}
			apiRespDoc, errDoc := s.bot.UploadFiles("sendDocument", docParams, []tgbotapi.RequestFile{docFile})
			if errDoc != nil {
				return nil, fmt.Errorf("%s failed (%v) and fallback sendDocument also failed: %w", field, err, errDoc)
			}
			var sentDoc tgbotapi.Message
			_ = json.Unmarshal(apiRespDoc.Result, &sentDoc)
			return &sentDoc, nil
		}
		return nil, err
	}

	var sent tgbotapi.Message
	_ = json.Unmarshal(apiResp.Result, &sent)
	return &sent, nil
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

func isAudioFile(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".mp3", ".m4a", ".ogg", ".wav":
		return true
	default:
		return false
	}
}
