package bot

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// DownloadTelegramFile fetches a file from Telegram API and saves it locally.
func DownloadTelegramFile(bot *tgbotapi.BotAPI, fileID string, destPath string) error {
	fileURL, err := bot.GetFileDirectURL(fileID)
	if err != nil {
		return fmt.Errorf("failed to get file direct URL: %w", err)
	}

	resp, err := http.Get(fileURL)
	if err != nil {
		return fmt.Errorf("failed to download file from telegram: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("telegram download returned status: %d", resp.StatusCode)
	}

	out, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("failed to create destination file: %w", err)
	}
	defer out.Close()

	_, err = io.Copy(out, resp.Body)
	return err
}

// DownloadedMediaItem holds metadata about a downloaded attachment.
type DownloadedMediaItem struct {
	FileName    string
	FilePath    string
	MediaType   string // "photo", "voice", "audio", "document"
	SizeBytes   int64
	Caption     string
	IsImage     bool
	IsText      bool
	TextContent string
}

// DownloadMessageMedia extracts and saves media from a Telegram message to disk with collision-proof naming.
func DownloadMessageMedia(bot *tgbotapi.BotAPI, msg *tgbotapi.Message, mediaDir string) (*DownloadedMediaItem, error) {
	if err := os.MkdirAll(mediaDir, 0755); err != nil {
		mediaDir = filepath.Join(os.TempDir(), "hermes_media")
		_ = os.MkdirAll(mediaDir, 0755)
	}

	caption := strings.TrimSpace(msg.Caption)
	nowUnix := time.Now().Unix()

	// 1. Photo
	if len(msg.Photo) > 0 {
		bestPhoto := msg.Photo[len(msg.Photo)-1]
		fileName := fmt.Sprintf("photo_%d_%d_%s.jpg", nowUnix, msg.MessageID, bestPhoto.FileID[:8])
		savePath := filepath.Join(mediaDir, fileName)

		if err := DownloadTelegramFile(bot, bestPhoto.FileID, savePath); err != nil {
			return nil, fmt.Errorf("gagal mengunduh foto: %w", err)
		}

		info, _ := os.Stat(savePath)
		size := int64(0)
		if info != nil {
			size = info.Size()
		}

		return &DownloadedMediaItem{
			FileName:  fileName,
			FilePath:  savePath,
			MediaType: "photo",
			SizeBytes: size,
			Caption:   caption,
			IsImage:   true,
		}, nil
	}

	// 2. Voice Note
	if msg.Voice != nil {
		fileName := fmt.Sprintf("voice_%d_%d_%s.ogg", nowUnix, msg.MessageID, msg.Voice.FileID[:8])
		savePath := filepath.Join(mediaDir, fileName)

		if err := DownloadTelegramFile(bot, msg.Voice.FileID, savePath); err != nil {
			return nil, fmt.Errorf("gagal mengunduh pesan suara: %w", err)
		}

		info, _ := os.Stat(savePath)
		size := int64(0)
		if info != nil {
			size = info.Size()
		}

		return &DownloadedMediaItem{
			FileName:  fileName,
			FilePath:  savePath,
			MediaType: "voice",
			SizeBytes: size,
			Caption:   caption,
		}, nil
	}

	// 3. Audio File
	if msg.Audio != nil {
		ext := filepath.Ext(msg.Audio.FileName)
		if ext == "" {
			ext = ".mp3"
		}
		rawBase := strings.TrimSuffix(filepath.Base(msg.Audio.FileName), ext)
		if rawBase == "" {
			rawBase = "audio"
		}
		fileName := fmt.Sprintf("audio_%d_%d_%s%s", nowUnix, msg.MessageID, rawBase, ext)
		savePath := filepath.Join(mediaDir, fileName)

		if err := DownloadTelegramFile(bot, msg.Audio.FileID, savePath); err != nil {
			return nil, fmt.Errorf("gagal mengunduh audio: %w", err)
		}

		info, _ := os.Stat(savePath)
		size := int64(0)
		if info != nil {
			size = info.Size()
		}

		return &DownloadedMediaItem{
			FileName:  fileName,
			FilePath:  savePath,
			MediaType: "audio",
			SizeBytes: size,
			Caption:   caption,
		}, nil
	}

	// 4. Document
	if msg.Document != nil {
		rawName := filepath.Base(msg.Document.FileName)
		if rawName == "" || rawName == "." {
			rawName = fmt.Sprintf("doc_%d.bin", msg.MessageID)
		}
		fileName := fmt.Sprintf("doc_%d_%d_%s", nowUnix, msg.MessageID, rawName)
		savePath := filepath.Join(mediaDir, fileName)

		if err := DownloadTelegramFile(bot, msg.Document.FileID, savePath); err != nil {
			return nil, fmt.Errorf("gagal mengunduh dokumen: %w", err)
		}

		info, _ := os.Stat(savePath)
		size := int64(0)
		if info != nil {
			size = info.Size()
		}

		ext := strings.ToLower(filepath.Ext(rawName))
		isTextFile := isTextExtension(ext) || strings.HasPrefix(msg.Document.MimeType, "text/")
		textContent := ""
		if isTextFile && size < 2*1024*1024 {
			if content, readErr := os.ReadFile(savePath); readErr == nil && len(content) > 0 {
				textContent = string(content)
			}
		}

		return &DownloadedMediaItem{
			FileName:    fileName,
			FilePath:    savePath,
			MediaType:   "document",
			SizeBytes:   size,
			Caption:     caption,
			IsText:      isTextFile && textContent != "",
			TextContent: textContent,
		}, nil
	}

	return nil, fmt.Errorf("pesan tidak mengandung media yang didukung")
}

// ProcessMediaMessage extracts media from a Telegram message, saves to disk, and returns the prompt and image path.
func ProcessMediaMessage(bot *tgbotapi.BotAPI, msg *tgbotapi.Message, mediaDir string) (prompt string, imagePath string, err error) {
	item, err := DownloadMessageMedia(bot, msg, mediaDir)
	if err != nil {
		return "", "", err
	}

	if item.IsImage {
		caption := item.Caption
		if caption == "" {
			caption = "Analisis gambar ini dan berikan penjelasan mendalam mengenai isinya."
		}
		return caption, item.FilePath, nil
	}

	if item.MediaType == "voice" {
		userPrompt := fmt.Sprintf("Pengguna mengirim pesan suara yang tersimpan di file: `%s`.\nTranskrip dan tanggapi pesan suara tersebut.", item.FilePath)
		if item.Caption != "" {
			userPrompt += "\nCatatan tambahan pengguna: " + item.Caption
		}
		return userPrompt, "", nil
	}

	if item.MediaType == "audio" {
		userPrompt := fmt.Sprintf("Pengguna mengirim file audio `%s` yang tersimpan di: `%s`.\nTolong periksa dan proses audio ini.", item.FileName, item.FilePath)
		if item.Caption != "" {
			userPrompt += "\nCatatan: " + item.Caption
		}
		return userPrompt, "", nil
	}

	if item.IsText && item.TextContent != "" {
		docPrompt := fmt.Sprintf("Pengguna mengirim file dokumen `%s` (tersimpan di `%s`).\n\nIsi file:\n```\n%s\n```",
			item.FileName, item.FilePath, item.TextContent)
		if item.Caption != "" {
			docPrompt += "\n\nInstruksi pengguna: " + item.Caption
		}
		return docPrompt, "", nil
	}

	userPrompt := fmt.Sprintf("Pengguna mengirim dokumen `%s` yang tersimpan di: `%s`.\nBaca dan analisis dokumen ini.", item.FileName, item.FilePath)
	if item.Caption != "" {
		userPrompt += "\nInstruksi tambahan: " + item.Caption
	}
	return userPrompt, "", nil
}

func isTextExtension(ext string) bool {
	switch ext {
	case ".txt", ".md", ".json", ".py", ".go", ".js", ".ts", ".jsx", ".tsx",
		".csv", ".log", ".sh", ".bash", ".yaml", ".yml", ".xml", ".html",
		".css", ".sql", ".env", ".toml", ".ini", ".conf", ".cfg":
		return true
	default:
		return false
	}
}
