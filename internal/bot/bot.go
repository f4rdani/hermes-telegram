package bot

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"hermes-tele/config"
	"hermes-tele/internal/engine"
	"hermes-tele/internal/session"
)

type QueuedTask struct {
	ChatID        int64
	UserID        int64
	Prompt        string
	ImagePath     string
	PreloadSkills string
	EnqueuedAt    time.Time
}

type BotServer struct {
	bot         *tgbotapi.BotAPI
	cfg         *config.Config
	sessMgr     *session.Manager
	runner      *engine.Runner
	cmdHandler  *CommandHandler
	version     string
	userLocks   sync.Map
	queueMu     sync.Mutex
	promptQueue map[int64][]QueuedTask
}

func NewBotServer(cfg *config.Config, version string) (*BotServer, error) {
	bot, err := tgbotapi.NewBotAPI(cfg.Telegram.BotToken)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize telegram bot: %w", err)
	}

	sessMgr := session.NewManager(
		cfg.Hermes.DefaultModel,
		"/root/apps/hermes-tele/sessions.json",
		cfg.Hermes.StateDBPath,
		cfg.Hermes.BinaryPath,
	)

	runner := engine.NewRunner(cfg.Hermes.BinaryPath)
	cmdHandler := NewCommandHandler(cfg, sessMgr, runner, version)

	server := &BotServer{
		bot:         bot,
		cfg:         cfg,
		sessMgr:     sessMgr,
		runner:      runner,
		cmdHandler:  cmdHandler,
		version:     version,
		promptQueue: make(map[int64][]QueuedTask),
	}

	server.registerCommands()

	return server, nil
}

func (s *BotServer) registerCommands() {
	setCmds := tgbotapi.NewSetMyCommands(HermesMenuCommands...)
	_, err := s.bot.Request(setCmds)
	if err != nil {
		log.Printf("[bot] Warning: Failed to register Telegram menu commands: %v", err)
	} else {
		log.Printf("[bot] Successfully registered %d Hermes menu commands with Telegram API", len(HermesMenuCommands))
	}
}

func (s *BotServer) enqueueTask(task QueuedTask) int {
	s.queueMu.Lock()
	defer s.queueMu.Unlock()
	s.promptQueue[task.UserID] = append(s.promptQueue[task.UserID], task)
	return len(s.promptQueue[task.UserID])
}

func (s *BotServer) popNextTask(userID int64) *QueuedTask {
	s.queueMu.Lock()
	defer s.queueMu.Unlock()
	q, ok := s.promptQueue[userID]
	if !ok || len(q) == 0 {
		return nil
	}
	next := q[0]
	s.promptQueue[userID] = q[1:]
	return &next
}

func (s *BotServer) listQueue(userID int64) []QueuedTask {
	s.queueMu.Lock()
	defer s.queueMu.Unlock()
	return append([]QueuedTask(nil), s.promptQueue[userID]...)
}

func (s *BotServer) clearQueue(userID int64) int {
	s.queueMu.Lock()
	defer s.queueMu.Unlock()
	count := len(s.promptQueue[userID])
	delete(s.promptQueue, userID)
	return count
}

func (s *BotServer) Start(ctx context.Context) error {
	log.Printf("[bot] Connected as @%s (Bot ID: %d)", s.bot.Self.UserName, s.bot.Self.ID)
	log.Printf("[bot] Authorized Telegram User IDs: %v", s.cfg.Telegram.AllowedUserIDs)
	log.Printf("[bot] Hermes Engine: %s | Default Model: %s | Workspace: %s",
		s.cfg.Hermes.BinaryPath, s.cfg.Hermes.DefaultModel, s.cfg.Hermes.WorkingDir)

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60

	updates := s.bot.GetUpdatesChan(u)

	for {
		select {
		case <-ctx.Done():
			log.Println("[bot] Shutting down Telegram bot cleanly...")
			return nil
		case update, ok := <-updates:
			if !ok {
				return nil
			}

			if update.CallbackQuery != nil {
				go s.handleCallbackQuery(update.CallbackQuery)
				continue
			}

			if update.Message != nil {
				go s.handleMessage(update.Message)
			}
		}
	}
}

func (s *BotServer) handleCallbackQuery(cb *tgbotapi.CallbackQuery) {
	userID := cb.From.ID
	chatID := cb.Message.Chat.ID
	data := cb.Data

	if !s.cfg.IsAllowed(userID) {
		ans := tgbotapi.NewCallback(cb.ID, "⛔ Akses ditolak")
		_, _ = s.bot.Request(ans)
		return
	}

	ans := tgbotapi.NewCallback(cb.ID, "")
	_, _ = s.bot.Request(ans)

	switch {
	case data == "cancel_task":
		if s.runner.Stop(userID) {
			_, _ = EditSafeMessage(s.bot, chatID, cb.Message.MessageID, "🛑 *Tugas telah dibatalkan oleh pengguna.*", nil)
		}

	case strings.HasPrefix(data, "set_model:"):
		modelName := strings.TrimPrefix(data, "set_model:")
		s.sessMgr.SetModel(userID, modelName)
		kb := ModelKeyboard(modelName)
		text := fmt.Sprintf("✅ *Model AI Berhasil Diubah*\n\nModel aktif sekarang: `%s`", modelName)
		_, _ = EditSafeMessage(s.bot, chatID, cb.Message.MessageID, text, &kb)

	case strings.HasPrefix(data, "set_reasoning:"):
		effort := strings.TrimPrefix(data, "set_reasoning:")
		s.sessMgr.SetReasoning(userID, effort)
		kb := ReasoningKeyboard(effort)
		text := fmt.Sprintf("✅ *Tingkat Penalaran Diubah*\n\nPengaturan aktif: `%s`", strings.ToUpper(effort))
		_, _ = EditSafeMessage(s.bot, chatID, cb.Message.MessageID, text, &kb)

	case strings.HasPrefix(data, "resume_session:"):
		sessionID := strings.TrimPrefix(data, "resume_session:")
		s.sessMgr.SetSessionID(userID, sessionID)
		text := fmt.Sprintf("✅ *Sesi Berhasil Dialihkan*\n\nSesi aktif saat ini: `%s`", sessionID)
		sent, _ := SendSafeMessage(s.bot, chatID, text, nil)
		s.sessMgr.AddTelegramMsgID(userID, sent.MessageID)

	case data == "new_session":
		s.sessMgr.ResetSession(userID)
		text := "✨ *Sesi Baru Disiapkan*\n\nKirim pesan untuk memulai percakapan baru dengan Aida."
		sent, _ := SendSafeMessage(s.bot, chatID, text, nil)
		s.sessMgr.AddTelegramMsgID(userID, sent.MessageID)

	case strings.HasPrefix(data, "cmd_page:"):
		pageStr := strings.TrimPrefix(data, "cmd_page:")
		page, _ := strconv.Atoi(pageStr)
		s.cmdHandler.HandleCommands(s.bot, chatID, page)
	}
}

func (s *BotServer) handleMessage(msg *tgbotapi.Message) {
	userID := msg.From.ID
	chatID := msg.Chat.ID
	text := strings.TrimSpace(msg.Text)

	if !s.cfg.IsAllowed(userID) {
		log.Printf("[bot] Access denied for UserID %d in ChatID %d", userID, chatID)
		_, _ = SendSafeMessage(s.bot, chatID, "⛔ *Akses Ditolak*\n\nUser ID Anda belum diizinkan dalam konfigurasi.", nil)
		return
	}

	// Track incoming user message ID for 50-turn Telegram window
	s.sessMgr.AddTelegramMsgID(userID, msg.MessageID)

	// 1. Check for Media message (photo, voice, audio, document)
	hasMedia := len(msg.Photo) > 0 || msg.Voice != nil || msg.Audio != nil || msg.Document != nil
	if hasMedia {
		prompt, imagePath, err := ProcessMediaMessage(s.bot, msg, s.cfg.Hermes.MediaDir)
		if err != nil {
			sent, _ := SendSafeMessage(s.bot, chatID, fmt.Sprintf("❌ Gagal memproses media: %v", err), nil)
			s.sessMgr.AddTelegramMsgID(userID, sent.MessageID)
			return
		}
		s.executeTask(chatID, userID, prompt, imagePath, "")
		return
	}

	if text == "" {
		return
	}

	// 2. Handle slash commands
	if strings.HasPrefix(text, "/") {
		s.dispatchCommand(msg, text)
		return
	}

	// 3. Regular chat prompt execution
	s.executeTask(chatID, userID, text, "", "")
}

func (s *BotServer) dispatchCommand(msg *tgbotapi.Message, rawText string) {
	chatID := msg.Chat.ID
	userID := msg.From.ID

	parts := strings.SplitN(rawText, " ", 2)
	cmd := strings.ToLower(parts[0])
	if atIdx := strings.Index(cmd, "@"); atIdx != -1 {
		cmd = cmd[:atIdx]
	}

	arg := ""
	if len(parts) > 1 {
		arg = strings.TrimSpace(parts[1])
	}

	switch cmd {
	case "/start":
		s.cmdHandler.HandleStart(s.bot, chatID, userID)
	case "/help":
		s.cmdHandler.HandleHelp(s.bot, chatID, userID, arg)
	case "/commands":
		page := 1
		if arg != "" {
			page, _ = strconv.Atoi(arg)
		}
		s.cmdHandler.HandleCommands(s.bot, chatID, page)
	case "/status":
		s.cmdHandler.HandleStatus(s.bot, chatID, userID)
	case "/context":
		s.cmdHandler.HandleContext(s.bot, chatID, userID)
	case "/model":
		s.cmdHandler.HandleModel(s.bot, chatID, userID, arg)
	case "/reasoning":
		s.cmdHandler.HandleReasoning(s.bot, chatID, userID, arg)
	case "/new":
		s.cmdHandler.HandleNew(s.bot, chatID, userID)
	case "/sessions":
		s.cmdHandler.HandleSessions(s.bot, chatID, userID)
	case "/resume":
		s.cmdHandler.HandleResume(s.bot, chatID, userID, arg)
	case "/title":
		s.cmdHandler.HandleTitle(s.bot, chatID, userID, arg)
	case "/clear":
		s.cmdHandler.HandleNew(s.bot, chatID, userID)
	case "/yolo":
		s.cmdHandler.HandleYolo(s.bot, chatID, userID)
	case "/busy":
		s.cmdHandler.HandleBusy(s.bot, chatID, userID, arg)
	case "/queue":
		s.handleQueueCommand(chatID, userID, arg)
	case "/clean", "/prune":
		s.handleCleanCommand(chatID, userID)
	case "/diff":
		s.cmdHandler.HandleDiff(s.bot, chatID)
	case "/skills":
		s.cmdHandler.HandleSkills(s.bot, chatID)
	case "/reload_skills":
		s.cmdHandler.HandleSkills(s.bot, chatID)
	case "/reload_mcp":
		sent, _ := SendSafeMessage(s.bot, chatID, "🔄 Konfigurasi MCP Server berhasil dimuat ulang.", nil)
		s.sessMgr.AddTelegramMsgID(userID, sent.MessageID)
	case "/whoami":
		s.cmdHandler.HandleWhoAmI(s.bot, chatID, userID, msg.From)
	case "/profile":
		s.cmdHandler.HandleProfile(s.bot, chatID)
	case "/version":
		s.cmdHandler.HandleVersion(s.bot, chatID)
	case "/stop":
		s.cmdHandler.HandleStop(s.bot, chatID, userID)
	case "/logs":
		s.cmdHandler.HandleLogs(s.bot, chatID)
	case "/retry":
		s.handleRetryCommand(chatID, userID, arg)
	case "/undo":
		s.handleUndoCommand(chatID, userID)
	default:
		skillName := strings.TrimPrefix(cmd, "/")
		skillsDir := filepath.Join(s.cfg.Hermes.HermesHome, "skills")
		if !skillExists(skillsDir, skillName) {
			sent, _ := SendSafeMessage(s.bot, chatID, fmt.Sprintf("❓ Perintah `/%s` tidak dikenali.\n\nKetik `/help` atau `/commands` untuk melihat daftar perintah yang tersedia.", skillName), nil)
			s.sessMgr.AddTelegramMsgID(userID, sent.MessageID)
			return
		}

		if arg != "" {
			s.executeTask(chatID, userID, arg, "", skillName)
		} else {
			s.executeTask(chatID, userID, fmt.Sprintf("Gunakan skill %s untuk membantu tugas berikutnya.", skillName), "", skillName)
		}
	}
}

func (s *BotServer) handleQueueCommand(chatID, userID int64, arg string) {
	arg = strings.TrimSpace(arg)
	if arg == "clear" {
		count := s.clearQueue(userID)
		sent, _ := SendSafeMessage(s.bot, chatID, fmt.Sprintf("🗑️ *Antrean Dikosongkan:*\n%d tugas di antrean telah dibatalkan.", count), nil)
		s.sessMgr.AddTelegramMsgID(userID, sent.MessageID)
		return
	}

	if arg == "" || arg == "list" {
		q := s.listQueue(userID)
		if len(q) == 0 {
			sent, _ := SendSafeMessage(s.bot, chatID, "ℹ️ Antrean kosong. Tidak ada pesan yang menunggu.", nil)
			s.sessMgr.AddTelegramMsgID(userID, sent.MessageID)
			return
		}
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("📋 *Daftar Antrean Prompt (%d tugas):*\n\n", len(q)))
		for i, t := range q {
			sb.WriteString(fmt.Sprintf("%d. `%s`\n   ⏱ _Diantrekan: %s_\n\n", i+1, truncate(t.Prompt, 50), t.EnqueuedAt.Format("15:04:05")))
		}
		sb.WriteString("_Ketik `/queue clear` untuk menghapus seluruh antrean._")
		sent, _ := SendSafeMessage(s.bot, chatID, sb.String(), nil)
		s.sessMgr.AddTelegramMsgID(userID, sent.MessageID)
		return
	}

	// Manual enqueue: /queue <prompt>
	pos := s.enqueueTask(QueuedTask{
		ChatID:     chatID,
		UserID:     userID,
		Prompt:     arg,
		EnqueuedAt: time.Now(),
	})
	reply := fmt.Sprintf("⏳ *Prompt Ditambahkan ke Antrean (#%d):*\n`%s`\n\nAkan otomatis dieksekusi begitu giliran saat ini selesai.", pos, truncate(arg, 60))
	sent, _ := SendSafeMessage(s.bot, chatID, reply, nil)
	s.sessMgr.AddTelegramMsgID(userID, sent.MessageID)
}

func (s *BotServer) handleCleanCommand(chatID, userID int64) {
	ids := s.sessMgr.ClearTelegramMsgIDs(userID)
	if len(ids) == 0 {
		sent, _ := SendSafeMessage(s.bot, chatID, "ℹ️ Tidak ada riwayat pesan lama yang tersimpan di memori cache Telegram.", nil)
		s.sessMgr.AddTelegramMsgID(userID, sent.MessageID)
		return
	}

	notice, _ := SendSafeMessage(s.bot, chatID, fmt.Sprintf("🧹 *Membersihkan %d pesan dari layar Telegram Anda...*\n_Memori konteks dan ingatan Aida di server tetap aman 100%%._", len(ids)), nil)
	s.sessMgr.AddTelegramMsgID(userID, notice.MessageID)

	go func(chat int64, toDel []int) {
		for _, id := range toDel {
			del := tgbotapi.NewDeleteMessage(chat, id)
			_, _ = s.bot.Send(del)
			time.Sleep(30 * time.Millisecond)
		}
	}(chatID, ids)
}

func (s *BotServer) handleRetryCommand(chatID, userID int64, arg string) {
	if s.runner.IsRunning(userID) {
		sent, _ := SendSafeMessage(s.bot, chatID, "⚠️ Masih ada tugas yang sedang berjalan. Tunggu hingga selesai atau gunakan `/stop`.", nil)
		s.sessMgr.AddTelegramMsgID(userID, sent.MessageID)
		return
	}

	userSess := s.sessMgr.Get(userID, chatID)
	lastPrompt, lastImg := s.sessMgr.GetLastPrompt(userID, userSess.CurrentSessionID)
	if lastPrompt == "" {
		sent, _ := SendSafeMessage(s.bot, chatID, "ℹ️ Belum ada pesan atau tugas sebelumnya yang dapat diulang (`/retry`).", nil)
		s.sessMgr.AddTelegramMsgID(userID, sent.MessageID)
		return
	}

	promptToRun := lastPrompt
	if arg != "" {
		promptToRun = fmt.Sprintf("%s\n\n(Catatan retry: %s)", lastPrompt, arg)
	}

	sent, _ := SendSafeMessage(s.bot, chatID, fmt.Sprintf("🔄 *Mengulang tugas sebelumnya:*\n`%s`", truncate(promptToRun, 80)), nil)
	s.sessMgr.AddTelegramMsgID(userID, sent.MessageID)

	go s.executeTask(chatID, userID, promptToRun, lastImg, "")
}

func (s *BotServer) handleUndoCommand(chatID, userID int64) {
	if s.runner.IsRunning(userID) {
		sent, _ := SendSafeMessage(s.bot, chatID, "⚠️ Tidak dapat melakukan `/undo` saat tugas sedang berjalan. Gunakan `/stop` terlebih dahulu.", nil)
		s.sessMgr.AddTelegramMsgID(userID, sent.MessageID)
		return
	}

	userSess := s.sessMgr.Get(userID, chatID)
	if userSess.CurrentSessionID == "" {
		sent, _ := SendSafeMessage(s.bot, chatID, "ℹ️ Tidak ada sesi aktif untuk di-undo.", nil)
		s.sessMgr.AddTelegramMsgID(userID, sent.MessageID)
		return
	}

	if err := s.sessMgr.UndoLastTurn(userSess.CurrentSessionID); err != nil {
		sent, _ := SendSafeMessage(s.bot, chatID, fmt.Sprintf("❌ Gagal melakukan undo: %v", err), nil)
		s.sessMgr.AddTelegramMsgID(userID, sent.MessageID)
		return
	}

	newPrompt, _ := s.sessMgr.GetLastPrompt(userID, userSess.CurrentSessionID)
	s.sessMgr.SetLastPrompt(userID, newPrompt, "")

	sent, _ := SendSafeMessage(s.bot, chatID, "↩️ *Turn Terakhir Berhasil Dibatalkan (Undo)*\nPesan terakhir dan balasannya telah dihapus dari riwayat konteks sesi ini.", nil)
	s.sessMgr.AddTelegramMsgID(userID, sent.MessageID)
}

func skillExists(skillsDir, skillName string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(skillName, "_", "-"))
	entries, err := os.ReadDir(skillsDir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if strings.ToLower(entry.Name()) == normalized {
			return true
		}
		subPath := filepath.Join(skillsDir, entry.Name())
		subEntries, err := os.ReadDir(subPath)
		if err != nil {
			continue
		}
		for _, sub := range subEntries {
			if sub.IsDir() && strings.ToLower(sub.Name()) == normalized {
				return true
			}
		}
	}
	return false
}

func (s *BotServer) executeTask(chatID, userID int64, prompt, imagePath, preloadSkills string) {
	lockIface, _ := s.userLocks.LoadOrStore(userID, &sync.Mutex{})
	lock := lockIface.(*sync.Mutex)

	if !lock.TryLock() {
		userSess := s.sessMgr.Get(userID, chatID)
		if userSess.BusyMode == "interrupt" {
			// Interrupt running task and run new prompt immediately
			sent, _ := SendSafeMessage(s.bot, chatID, "🛑 *Menginterupsi tugas lama untuk menjalankan pesan baru...*", nil)
			s.sessMgr.AddTelegramMsgID(userID, sent.MessageID)
			s.runner.Stop(userID)
			go func() {
				time.Sleep(600 * time.Millisecond)
				s.executeTask(chatID, userID, prompt, imagePath, preloadSkills)
			}()
			return
		}

		// Mode queue: FIFO enqueue
		pos := s.enqueueTask(QueuedTask{
			ChatID:        chatID,
			UserID:        userID,
			Prompt:        prompt,
			ImagePath:     imagePath,
			PreloadSkills: preloadSkills,
			EnqueuedAt:    time.Now(),
		})
		reply := fmt.Sprintf("⏳ *Pesan Masuk Antrean (Urutan #%d):*\n`%s`\n\n_Aida sedang menyelesaikan tugas sebelumnya. Pesan ini akan otomatis dieksekusi setelah selesai._\n_Gunakan `/stop` untuk membatalkan tugas yang sedang berjalan._", pos, truncate(prompt, 60))
		sent, _ := SendSafeMessage(s.bot, chatID, reply, nil)
		s.sessMgr.AddTelegramMsgID(userID, sent.MessageID)
		return
	}
	defer lock.Unlock()

	userSess := s.sessMgr.Get(userID, chatID)
	s.sessMgr.SetLastPrompt(userID, prompt, imagePath)

	cancelKb := CancelKeyboard()
	statusMsg, err := SendSafeMessage(s.bot, chatID, "🌸 *Aida sedang menjalankan tugas...*\n\n💭 _Sedang berpikir & menganalisis instruksi..._", cancelKb)
	hasStatusMsg := (err == nil)
	if hasStatusMsg {
		s.sessMgr.AddTelegramMsgID(userID, statusMsg.MessageID)
	}

	stopTyping := make(chan struct{})
	go func() {
		ticker := time.NewTicker(4 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stopTyping:
				return
			case <-ticker.C:
				action := tgbotapi.NewChatAction(chatID, tgbotapi.ChatTyping)
				_, _ = s.bot.Send(action)
			}
		}
	}()

	startTime := time.Now()
	log.Printf("[bot] Task starting for User %d (Session: %s, Model: %s)",
		userID, userSess.CurrentSessionID, userSess.CurrentModel)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	opts := engine.RunOptions{
		Prompt:          prompt,
		ImagePath:       imagePath,
		SessionID:       userSess.CurrentSessionID,
		Model:           userSess.CurrentModel,
		ReasoningEffort: userSess.ReasoningEffort,
		Yolo:            userSess.YoloMode,
		PreloadSkills:   preloadSkills,
		WorkingDir:      s.cfg.Hermes.WorkingDir,
		OnProgress: func(displayText string) {
			if hasStatusMsg {
				_, _ = EditSafeMessage(s.bot, chatID, statusMsg.MessageID, displayText, &cancelKb)
			}
		},
	}

	result, runErr := s.runner.Execute(ctx, userID, opts)
	close(stopTyping)
	duration := time.Since(startTime).Round(time.Millisecond)

	if runErr != nil {
		log.Printf("[bot] Task error (%v): %v", duration, runErr)
		errMsg := fmt.Sprintf("❌ *Gagal Mengeksekusi Tugas (%v):*\n\n```\n%v\n```", duration, runErr)
		if hasStatusMsg {
			_, _ = EditSafeMessage(s.bot, chatID, statusMsg.MessageID, errMsg, nil)
		} else {
			sent, _ := SendSafeMessage(s.bot, chatID, errMsg, nil)
			s.sessMgr.AddTelegramMsgID(userID, sent.MessageID)
		}
		s.pruneOldTelegramMessages(chatID, userID)
		s.checkAndRunNextQueuedTask(userID)
		return
	}

	// Update session ID if generated/changed
	if result != nil && result.SessionID != "" {
		s.sessMgr.SetSessionID(userID, result.SessionID)
	}

	finalText := ""
	if result != nil {
		finalText = strings.TrimSpace(result.FinalText)
	}

	if finalText == "" {
		finalText = fmt.Sprintf("✅ *Tugas selesai dalam %v tanpa keluaran teks.*", duration)
	}

	activeSessionID := userSess.CurrentSessionID
	if result != nil && result.SessionID != "" {
		activeSessionID = result.SessionID
	}

	footer := s.formatResultFooter(duration, result, activeSessionID)
	fullResponse := finalText + footer

	// Deliver response chunks (Telegram limit 4096)
	chunks := SplitMessage(fullResponse, 4000)
	if hasStatusMsg && len(chunks) > 0 {
		_, _ = EditSafeMessage(s.bot, chatID, statusMsg.MessageID, chunks[0], nil)
		for _, chunk := range chunks[1:] {
			sent, _ := SendSafeMessage(s.bot, chatID, chunk, nil)
			s.sessMgr.AddTelegramMsgID(userID, sent.MessageID)
		}
	} else {
		for _, chunk := range chunks {
			sent, _ := SendSafeMessage(s.bot, chatID, chunk, nil)
			s.sessMgr.AddTelegramMsgID(userID, sent.MessageID)
		}
	}

	log.Printf("[bot] Task completed successfully in %v (Session: %s, Chunks: %d)",
		duration, userSess.CurrentSessionID, len(chunks))

	// Prune old messages in Telegram chat (max 50 turns = 100 messages)
	s.pruneOldTelegramMessages(chatID, userID)

	// Check and run next queued task FIFO!
	s.checkAndRunNextQueuedTask(userID)
}

func (s *BotServer) checkAndRunNextQueuedTask(userID int64) {
	if next := s.popNextTask(userID); next != nil {
		log.Printf("[bot] Running next queued task for User %d: %q", userID, next.Prompt)
		go s.executeTask(next.ChatID, next.UserID, next.Prompt, next.ImagePath, next.PreloadSkills)
	}
}

func (s *BotServer) pruneOldTelegramMessages(chatID, userID int64) {
	// 50 turns = 50 user prompts + 50 bot replies = 100 messages total in Telegram
	toDelete := s.sessMgr.PopOldTelegramMsgIDs(userID, 100)
	if len(toDelete) == 0 {
		return
	}

	go func(chat int64, ids []int) {
		for _, id := range ids {
			del := tgbotapi.NewDeleteMessage(chat, id)
			_, _ = s.bot.Send(del)
			time.Sleep(30 * time.Millisecond)
		}
		log.Printf("[bot] Pruned %d old Telegram messages in chat %d to maintain 50 turns window", len(ids), chat)
	}(chatID, toDelete)
}

func truncate(s string, max int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > max {
		return s[:max] + "..."
	}
	return s
}

func (s *BotServer) formatResultFooter(duration time.Duration, result *engine.RunResult, sessionID string) string {
	var parts []string

	// 1. Duration
	durSec := duration.Seconds()
	if result != nil && result.DurationMs > 0 {
		durSec = float64(result.DurationMs) / 1000.0
	}
	if durSec > 0 {
		parts = append(parts, fmt.Sprintf("⏱ %.1fs", durSec))
	}

	// 2. Tokens & Turns from Session Details in state.db
	totalTokens := 0
	turns := 1
	if sessionID != "" {
		if details, err := s.sessMgr.GetSessionDetails(sessionID); err == nil && details != nil {
			totalTokens = details.InputTokens + details.OutputTokens
			if details.MessageCount > 0 {
				turns = (details.MessageCount + 1) / 2
			}
		}
	}
	if totalTokens == 0 && result != nil && result.Tokens.Total > 0 {
		totalTokens = result.Tokens.Total
	}
	if totalTokens > 0 {
		parts = append(parts, fmt.Sprintf("🪙 %d tokens", totalTokens))
	}

	if turns > 0 {
		parts = append(parts, fmt.Sprintf("🔄 Turn %d", turns))
	}

	// 3. Short Session ID
	if sessionID != "" {
		shortID := sessionID
		if partsID := strings.Split(sessionID, "_"); len(partsID) >= 3 {
			shortID = partsID[2]
		} else if len(shortID) > 8 {
			shortID = shortID[:8]
		}
		shortID = strings.ReplaceAll(shortID, "_", "-")
		parts = append(parts, fmt.Sprintf("🆔 %s", shortID))
	}

	// 4. Version
	ver := s.version
	if !strings.HasPrefix(ver, "v") {
		ver = "v" + ver
	}
	parts = append(parts, fmt.Sprintf("🏷️ %s", ver))

	return "\n\n_(" + strings.Join(parts, " • ") + ")_"
}

