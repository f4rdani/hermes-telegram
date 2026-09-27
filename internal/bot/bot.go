package bot

import (
	"context"
	"errors"
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
	aggregator  *MessageAggregator
	approvals   *ApprovalStore
	// Background tasks (/bg): own registry, independent of the foreground per-user lock.
	bgMu    sync.Mutex
	bgTasks map[string]*BackgroundTask
	bgSeq   int64
	// Pending steer text (/steer): injected as followup after the running task ends.
	steerMu      sync.Mutex
	pendingSteer map[int64]string
	// Global pause (/pause): new work is held while paused.
	pausedMu sync.RWMutex
	paused   bool
}

func NewBotServer(cfg *config.Config, configPath, version string) (*BotServer, error) {
	bot, err := tgbotapi.NewBotAPI(cfg.Telegram.BotToken)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize telegram bot: %w", err)
	}

	sessMgr := session.NewManager(
		cfg.Hermes.DefaultModel,
		cfg.Hermes.SessionsPath,
		cfg.Hermes.StateDBPath,
		cfg.Hermes.BinaryPath,
	)

	modelResolver := NewModelResolver(cfg.Hermes.GatewayURL, cfg.Hermes.GatewayKey)
	runner := engine.NewRunner(cfg.Hermes.BinaryPath)
	cmdHandler := NewCommandHandler(cfg, sessMgr, runner, modelResolver, version)

	server := &BotServer{
		bot:          bot,
		cfg:          cfg,
		sessMgr:      sessMgr,
		runner:       runner,
		cmdHandler:   cmdHandler,
		version:      version,
		promptQueue:  make(map[int64][]QueuedTask),
		aggregator:   NewMessageAggregator(),
		approvals:    NewApprovalStore(ApprovalTimeout),
		bgTasks:      make(map[string]*BackgroundTask),
		pendingSteer: make(map[int64]string),
	}
	server.cmdHandler.SetBgLinesProvider(server.listBackgroundLines)
	server.cmdHandler.SetConfigPath(configPath)

	server.registerCommands()

	return server, nil
}

func (s *BotServer) registerCommands() {
	cmds := HermesMenuCommands
	if len(cmds) > 99 {
		cmds = cmds[:99]
	}
	setCmds := tgbotapi.NewSetMyCommands(cmds...)
	_, err := s.bot.Request(setCmds)
	if err != nil {
		log.Printf("[bot] Warning: Failed to register Telegram menu commands: %v", err)
	} else {
		log.Printf("[bot] Successfully registered %d Hermes menu commands with Telegram API", len(cmds))
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

// queueRemove deletes the 0-based item. Returns the removed task and true on success.
func (s *BotServer) queueRemove(userID int64, idx int) (QueuedTask, bool) {
	s.queueMu.Lock()
	defer s.queueMu.Unlock()
	q := s.promptQueue[userID]
	if idx < 0 || idx >= len(q) {
		return QueuedTask{}, false
	}
	removed := q[idx]
	s.promptQueue[userID] = append(q[:idx], q[idx+1:]...)
	return removed, true
}

// queueEdit replaces the prompt of the 0-based item.
func (s *BotServer) queueEdit(userID int64, idx int, prompt string) bool {
	s.queueMu.Lock()
	defer s.queueMu.Unlock()
	q := s.promptQueue[userID]
	if idx < 0 || idx >= len(q) {
		return false
	}
	q[idx].Prompt = prompt
	return true
}

// queueMove relocates the 0-based item from→to (to is clamped into range).
func (s *BotServer) queueMove(userID int64, from, to int) bool {
	s.queueMu.Lock()
	defer s.queueMu.Unlock()
	q := s.promptQueue[userID]
	if from < 0 || from >= len(q) || to < 0 || to >= len(q) {
		return false
	}
	if from == to {
		return true
	}
	item := q[from]
	q = append(q[:from], q[from+1:]...)
	if to > len(q) {
		to = len(q)
	}
	q = append(q, QueuedTask{})
	copy(q[to+1:], q[to:])
	q[to] = item
	s.promptQueue[userID] = q
	return true
}

// pushFrontTask inserts a task at the head of the queue (used for steer follow-ups).
func (s *BotServer) pushFrontTask(task QueuedTask) {
	s.queueMu.Lock()
	defer s.queueMu.Unlock()
	q := s.promptQueue[task.UserID]
	q = append([]QueuedTask{task}, q...)
	s.promptQueue[task.UserID] = q
}

func (s *BotServer) clearQueue(userID int64) int {
	s.queueMu.Lock()
	defer s.queueMu.Unlock()
	count := len(s.promptQueue[userID])
	delete(s.promptQueue, userID)
	return count
}

// isPauseCommand reports whether text invokes /pause (with optional @bot suffix).
func isPauseCommand(text string) bool {
	fields := strings.Fields(strings.TrimSpace(text))
	if len(fields) == 0 {
		return false
	}
	cmd := strings.ToLower(fields[0])
	if i := strings.Index(cmd, "@"); i != -1 {
		cmd = cmd[:i]
	}
	return cmd == "/pause"
}

func (s *BotServer) isPaused() bool {
	s.pausedMu.RLock()
	defer s.pausedMu.RUnlock()
	return s.paused
}

func (s *BotServer) setPaused(p bool) {
	s.pausedMu.Lock()
	defer s.pausedMu.Unlock()
	s.paused = p
}

func (s *BotServer) Start(ctx context.Context) error {
	log.Printf("[bot] Connected as @%s (Bot ID: %d)", s.bot.Self.UserName, s.bot.Self.ID)
	log.Printf("[bot] Authorized Telegram User IDs: %v", s.cfg.Telegram.AllowedUserIDs)
	log.Printf("[bot] Hermes Engine: %s | Default Model: %s | Workspace: %s",
		s.cfg.Hermes.BinaryPath, s.cfg.Hermes.DefaultModel, s.cfg.Hermes.WorkingDir)

	go s.CheckAndNotifyRestart()

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

	switch {
	case data == "cancel_task":
		s.aggregator.Cancel(userID)
		s.takePendingSteer(userID)
		s.withdrawSelfRestartApproval(userID, "Tugas dibatalkan — perintah TIDAK dijalankan.")
		s.stopUserBackgroundTasks(userID)
		if s.runner.Stop(userID) {
			_, _ = EditSafeMessage(s.bot, chatID, cb.Message.MessageID, "🛑 *Tugas telah dibatalkan oleh pengguna.*", nil)
		}
		_, _ = s.bot.Request(tgbotapi.NewCallback(cb.ID, "🛑 Tugas dibatalkan"))

	case strings.HasPrefix(data, "set_model:"):
		modelName := strings.TrimPrefix(data, "set_model:")
		s.sessMgr.SetModel(userID, modelName)
		// Delete menu message to keep the chat completely clean
		_, _ = s.bot.Send(tgbotapi.NewDeleteMessage(chatID, cb.Message.MessageID))
		_, _ = s.bot.Request(tgbotapi.NewCallback(cb.ID, fmt.Sprintf("✅ Model aktif: %s", modelName)))

	case strings.HasPrefix(data, "set_reasoning:"):
		effort := strings.TrimPrefix(data, "set_reasoning:")
		s.sessMgr.SetReasoning(userID, effort)
		// Delete menu message to keep the chat completely clean
		_, _ = s.bot.Send(tgbotapi.NewDeleteMessage(chatID, cb.Message.MessageID))
		_, _ = s.bot.Request(tgbotapi.NewCallback(cb.ID, fmt.Sprintf("✅ Penalaran: %s", strings.ToUpper(effort))))

	case data == "dismiss_msg" || data == "delete_msg" || data == "cancel_menu":
		_, _ = s.bot.Send(tgbotapi.NewDeleteMessage(chatID, cb.Message.MessageID))
		_, _ = s.bot.Request(tgbotapi.NewCallback(cb.ID, "✖️ Menu ditutup"))

	case strings.HasPrefix(data, "resume_session:"):
		sessionID := strings.TrimPrefix(data, "resume_session:")
		s.sessMgr.SetSessionID(userID, sessionID)
		// Delete menu message to keep the chat completely clean
		_, _ = s.bot.Send(tgbotapi.NewDeleteMessage(chatID, cb.Message.MessageID))
		_, _ = s.bot.Request(tgbotapi.NewCallback(cb.ID, fmt.Sprintf("✅ Beralih ke sesi: %s", sessionID)))

	case data == "run_update_now":
		_, _ = s.bot.Send(tgbotapi.NewDeleteMessage(chatID, cb.Message.MessageID))
		_, _ = s.bot.Request(tgbotapi.NewCallback(cb.ID, "🚀 Memulai pembaruan..."))
		s.cmdHandler.HandleUpdate(s.bot, chatID, "now")

	case data == "new_session":
		s.sessMgr.ResetSession(userID)
		// Delete menu message to keep the chat completely clean
		_, _ = s.bot.Send(tgbotapi.NewDeleteMessage(chatID, cb.Message.MessageID))
		_, _ = s.bot.Request(tgbotapi.NewCallback(cb.ID, "✨ Sesi baru telah disiapkan"))

	case strings.HasPrefix(data, "cmd_page:"):
		pageStr := strings.TrimPrefix(data, "cmd_page:")
		page, _ := strconv.Atoi(pageStr)
		s.cmdHandler.HandleCommands(s.bot, chatID, page)
		_, _ = s.bot.Request(tgbotapi.NewCallback(cb.ID, ""))

	case strings.HasPrefix(data, "approve:sr:"):
		requestID := strings.TrimPrefix(data, "approve:sr:")
		s.resolveSelfRestartApproval(cb.ID, chatID, requestID, true, "")

	case strings.HasPrefix(data, "deny:sr:"):
		requestID := strings.TrimPrefix(data, "deny:sr:")
		s.resolveSelfRestartApproval(cb.ID, chatID, requestID, false, "")

	default:
		_, _ = s.bot.Request(tgbotapi.NewCallback(cb.ID, ""))
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

	// Global pause (/pause): hold all new work except /pause itself.
	if s.isPaused() && !isPauseCommand(text) {
		sent, _ := SendSafeMessage(s.bot, chatID, "⏸️ *Gateway Dijeda.*\nPesan/media diabaikan. Gunakan `/pause off` untuk melanjutkan.", nil)
		s.sessMgr.AddTelegramMsgID(userID, sent.MessageID)
		return
	}

	// Track incoming user message ID as the start of a turn for 50-turn Telegram window
	s.sessMgr.StartTurn(userID, msg.MessageID)

	// 1. Check for Media message (photo, voice, audio, document)
	hasMedia := len(msg.Photo) > 0 || msg.Voice != nil || msg.Audio != nil || msg.Document != nil
	if hasMedia {
		prompt, imagePath, err := ProcessMediaMessage(s.bot, msg, s.cfg.Hermes.MediaDir)
		if err != nil {
			sent, _ := SendSafeMessage(s.bot, chatID, fmt.Sprintf("❌ Gagal memproses media: %v", err), nil)
			s.sessMgr.AddTelegramMsgID(userID, sent.MessageID)
			return
		}
		s.executeTask(chatID, userID, prompt, imagePath, "", 1)
		return
	}

	if text == "" {
		return
	}

	// 2. Handle slash commands
	if strings.HasPrefix(text, "/") {
		s.aggregator.Cancel(userID)
		s.dispatchCommand(msg, text)
		return
	}

	// 3. Regular chat prompt execution via MessageAggregator (auto-stitches Telegram 4096-char split chunks)
	s.aggregator.Add(msg, func(cID, uID int64, prompt, img, skills string, partsCount int) {
		s.executeTask(cID, uID, prompt, img, skills, partsCount)
	})
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

	// Immediate typing action feedback for all slash commands
	_, _ = s.bot.Send(tgbotapi.NewChatAction(chatID, tgbotapi.ChatTyping))

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
	case "/queue", "/q":
		s.handleQueueCommand(chatID, userID, arg)
	case "/clean", "/prune":
		s.handleCleanCommand(chatID, userID)
	case "/autodelete":
		if arg == "clean" || arg == "clear" {
			s.handleCleanCommand(chatID, userID)
		} else {
			s.cmdHandler.HandleAutoDelete(s.bot, chatID, userID, arg)
		}
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
		s.aggregator.Cancel(userID)
		s.takePendingSteer(userID)
		s.withdrawSelfRestartApproval(userID, "Tugas dibatalkan via /stop — perintah TIDAK dijalankan.")
		if n := s.stopUserBackgroundTasks(userID); n > 0 {
			sent, _ := SendSafeMessage(s.bot, chatID, fmt.Sprintf("🛑 *%d background task ikut dibatalkan.*", n), nil)
			s.sessMgr.AddTelegramMsgID(userID, sent.MessageID)
		}
		s.cmdHandler.HandleStop(s.bot, chatID, userID)
	case "/ps", "/processes", "/procs":
		s.cmdHandler.HandleProcessStatus(s.bot, chatID)
	case "/logs":
		s.cmdHandler.HandleLogs(s.bot, chatID)
	case "/retry":
		s.handleRetryCommand(chatID, userID, arg)
	case "/undo":
		s.handleUndoCommand(chatID, userID)
	case "/egress":
		s.cmdHandler.HandleEgress(s.bot, chatID)
	case "/debug":
		s.cmdHandler.HandleDebug(s.bot, chatID, arg)
	case "/usage":
		s.cmdHandler.HandleUsage(s.bot, chatID, userID, arg)
	case "/approve":
		s.handleApproveTextCommand(chatID, userID, arg)
	case "/deny":
		s.handleDenyTextCommand(chatID, userID, arg)
	case "/compress", "/compact":
		s.cmdHandler.HandleCompress(s.bot, chatID, userID, arg)
	case "/sendfile", "/send":
		s.cmdHandler.HandleSendFile(s.bot, chatID, userID, arg)
	case "/restart":
		s.cmdHandler.HandleRestart(s.bot, chatID)
	case "/update":
		s.cmdHandler.HandleUpdate(s.bot, chatID, arg)
	case "/save", "/export":
		s.cmdHandler.HandleSave(s.bot, chatID, userID, arg)
	case "/rollback":
		s.cmdHandler.HandleRollback(s.bot, chatID, arg)
	case "/branch", "/fork":
		s.cmdHandler.HandleBranch(s.bot, chatID, userID, arg)
	case "/pause":
		lowArg := strings.ToLower(strings.TrimSpace(arg))
		if lowArg == "off" || lowArg == "resume" || lowArg == "0" || lowArg == "no" {
			s.setPaused(false)
		} else {
			s.setPaused(true)
		}
		s.cmdHandler.HandlePause(s.bot, chatID, arg)
	case "/agents", "/tasks":
		s.cmdHandler.HandleAgents(s.bot, chatID, userID)
	case "/memory":
		s.cmdHandler.HandleMemory(s.bot, chatID, arg)
	case "/bundles":
		s.cmdHandler.HandleBundles(s.bot, chatID)
	case "/platform", "/platforms":
		s.cmdHandler.HandlePlatform(s.bot, chatID)
	case "/voice":
		s.cmdHandler.HandleVoice(s.bot, chatID, arg)
	case "/personality":
		s.cmdHandler.HandlePersonality(s.bot, chatID, arg)
	case "/fast":
		s.cmdHandler.HandleFast(s.bot, chatID, arg)
	case "/approvals":
		s.cmdHandler.HandleApprovals(s.bot, chatID, userID, arg)
	case "/insights":
		s.cmdHandler.HandleInsights(s.bot, chatID, arg)
	case "/curator":
		s.cmdHandler.HandleCurator(s.bot, chatID)
	case "/kanban":
		s.cmdHandler.HandleKanban(s.bot, chatID)
	case "/topic":
		s.cmdHandler.HandleTopic(s.bot, chatID)
	case "/sethome", "/set-home":
		s.cmdHandler.HandleSetHome(s.bot, chatID)
	case "/codex_runtime", "/codex-runtime":
		s.cmdHandler.HandleCodexRuntime(s.bot, chatID, arg)
	case "/footer":
		s.cmdHandler.HandleFooter(s.bot, chatID, arg)
	case "/suggestions", "/suggest":
		s.cmdHandler.HandleSuggestions(s.bot, chatID, arg)
	case "/blueprint", "/bp":
		s.cmdHandler.HandleBlueprint(s.bot, chatID, arg)
	case "/login":
		s.cmdHandler.HandleLogin(s.bot, chatID)
	case "/topup":
		s.cmdHandler.HandleTopup(s.bot, chatID)
	case "/heartbeat", "/hb":
		s.cmdHandler.HandleHeartbeat(s.bot, chatID, arg)
	case "/loop", "/proactive":
		s.cmdHandler.HandleLoop(s.bot, chatID, arg)
	case "/goal":
		if arg == "" {
			sent, _ := SendSafeMessage(s.bot, chatID, "🎯 *Target Goal Hermes:*\n\nFormat penggunaan: `/goal <deskripsi target>`\nHermes akan bekerja secara persisten melintasi turn sampai target tercapai.", nil)
			s.sessMgr.AddTelegramMsgID(userID, sent.MessageID)
			return
		}
		s.executeTask(chatID, userID, fmt.Sprintf("Target Goal: %s\nKerjakan secara bertahap sampai tujuan ini tercapai.", arg), "", "", 1)
	case "/plan":
		if arg == "" {
			sent, _ := SendSafeMessage(s.bot, chatID, "📝 *Perencanaan Implementasi (Plan):*\n\nFormat: `/plan <tugas atau fitur>`\nHermes akan menyusun rencana implementasi markdown terperinci tanpa mengeksekusi aksi berbahaya.", nil)
			s.sessMgr.AddTelegramMsgID(userID, sent.MessageID)
			return
		}
		s.executeTask(chatID, userID, fmt.Sprintf("Buatkan rencana implementasi (architecture & action plan) lengkap dalam markdown untuk: %s\nJangan langsung eksekusi atau merubah file, fokus pada analisa dan rencana tindakan.", arg), "", "", 1)
	case "/review":
		prompt := "Tinjau (review) pekerjaan dan kode yang telah dikerjakan di sesi ini secara kritis. Berikan catatan kualitas, temuan bug, dan rekomendasi penyempurnaan."
		if arg != "" {
			prompt = fmt.Sprintf("Tinjau (review) hal berikut: %s\nFokus pada kebenaran arsitektur, keamanan, dan efisiensi.", arg)
		}
		s.executeTask(chatID, userID, prompt, "", "", 1)
	case "/refine":
		prompt := "Tinjau percakapan sesi ini dan simpan pelajaran atau wawasan penting ke memori."
		if arg != "" {
			prompt += fmt.Sprintf(" Fokus: %s", arg)
		}
		s.executeTask(chatID, userID, prompt, "", "", 1)
	case "/moa":
		if arg == "" {
			sent, _ := SendSafeMessage(s.bot, chatID, "🧬 *Mixture of Agents (MoA):*\n\nFormat: `/moa <prompt>`\nMenjalankan satu prompt melalui perpaduan model.", nil)
			s.sessMgr.AddTelegramMsgID(userID, sent.MessageID)
			return
		}
		s.executeTask(chatID, userID, arg, "", "", 1)
	case "/subgoal":
		if arg == "" {
			sent, _ := SendSafeMessage(s.bot, chatID, "🎯 *Kriteria Subgoal:*\n\nFormat: `/subgoal <kriteria tambahan pada goal>`", nil)
			s.sessMgr.AddTelegramMsgID(userID, sent.MessageID)
			return
		}
		s.executeTask(chatID, userID, fmt.Sprintf("Tambahkan kriteria subgoal berikut: %s", arg), "", "", 1)
	case "/learn":
		if arg == "" {
			sent, _ := SendSafeMessage(s.bot, chatID, "🎓 *Hermes Skill Learning:*\n\nFormat: `/learn <hal atau dokumentasi yang ingin dipelajari>`\nHermes akan membuat skill reusable baru di ~/.hermes/skills/", nil)
			s.sessMgr.AddTelegramMsgID(userID, sent.MessageID)
			return
		}
		s.executeTask(chatID, userID, fmt.Sprintf("Pelajari dan buat skill reusable baru untuk: %s\nSimpan dokumen SKILL.md ke ~/.hermes/skills/", arg), "", "", 1)
	case "/init":
		prompt := "Pindai repositori proyek ini dan perbarui atau buat file AGENTS.md dengan panduan instruksi proyek yang lengkap."
		if arg != "" {
			prompt += fmt.Sprintf(" Catatan tambahan: %s", arg)
		}
		s.executeTask(chatID, userID, prompt, "", "", 1)
	case "/bg":
		s.handleBgCommand(chatID, userID, arg)
	case "/btw":
		if arg == "" {
			sent, _ := SendSafeMessage(s.bot, chatID, "💬 *Pertanyaan Sampingan (By The Way):*\n\nFormat: `/btw <pertanyaan singkat>`\nMenanyakan pertanyaan sampingan tanpa merusak alur fokus.", nil)
			s.sessMgr.AddTelegramMsgID(userID, sent.MessageID)
			return
		}
		s.executeTask(chatID, userID, fmt.Sprintf("[Pertanyaan Sampingan (BTW)]: %s", arg), "", "", 1)
	case "/steer":
		s.handleSteerCommand(chatID, userID, arg)
	default:
		skillName := strings.TrimPrefix(cmd, "/")
		skillsDir := filepath.Join(s.cfg.Hermes.HermesHome, "skills")
		if !skillExists(skillsDir, skillName) {
			sent, _ := SendSafeMessage(s.bot, chatID, fmt.Sprintf("❓ Perintah `/%s` tidak dikenali.\n\nKetik `/help` atau `/commands` untuk melihat daftar perintah yang tersedia.", skillName), nil)
			s.sessMgr.AddTelegramMsgID(userID, sent.MessageID)
			return
		}

		if arg != "" {
			s.executeTask(chatID, userID, arg, "", skillName, 1)
		} else {
			s.executeTask(chatID, userID, fmt.Sprintf("Gunakan skill %s untuk membantu tugas berikutnya.", skillName), "", skillName, 1)
		}
	}
}

func (s *BotServer) handleQueueCommand(chatID, userID int64, arg string) {
	arg = strings.TrimSpace(arg)
	if arg == "" || strings.EqualFold(arg, "list") {
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
		sb.WriteString("_Kelola:_ `/queue edit N <prompt>` • `/queue rm N` • `/queue move A B` • `/queue clear`")
		sent, _ := SendSafeMessage(s.bot, chatID, sb.String(), nil)
		s.sessMgr.AddTelegramMsgID(userID, sent.MessageID)
		return
	}

	fields := strings.Fields(arg)
	sub := strings.ToLower(fields[0])

	switch sub {
	case "clear":
		count := s.clearQueue(userID)
		sent, _ := SendSafeMessage(s.bot, chatID, fmt.Sprintf("🗑️ *Antrean Dikosongkan:*\n%d tugas di antrean telah dibatalkan.", count), nil)
		s.sessMgr.AddTelegramMsgID(userID, sent.MessageID)
		return
	case "rm", "remove", "del", "delete":
		if len(fields) < 2 {
			sent, _ := SendSafeMessage(s.bot, chatID, "ℹ️ Format: `/queue rm N` — hapus item antrean nomor N. Lihat nomor via `/queue list`.", nil)
			s.sessMgr.AddTelegramMsgID(userID, sent.MessageID)
			return
		}
		n, err := strconv.Atoi(fields[1])
		if err != nil {
			sent, _ := SendSafeMessage(s.bot, chatID, fmt.Sprintf("❌ Nomor tidak valid: `%s`.", fields[1]), nil)
			s.sessMgr.AddTelegramMsgID(userID, sent.MessageID)
			return
		}
		removed, ok := s.queueRemove(userID, n-1)
		if !ok {
			sent, _ := SendSafeMessage(s.bot, chatID, fmt.Sprintf("❌ Item #%d tidak ada. Cek `/queue list`.", n), nil)
			s.sessMgr.AddTelegramMsgID(userID, sent.MessageID)
			return
		}
		sent, _ := SendSafeMessage(s.bot, chatID, fmt.Sprintf("🗑️ *Item #%d Dihapus:*\n`%s`", n, truncate(removed.Prompt, 80)), nil)
		s.sessMgr.AddTelegramMsgID(userID, sent.MessageID)
		return
	case "edit":
		if len(fields) < 3 {
			sent, _ := SendSafeMessage(s.bot, chatID, "ℹ️ Format: `/queue edit N <prompt baru>` — ubah isi item antrean nomor N.", nil)
			s.sessMgr.AddTelegramMsgID(userID, sent.MessageID)
			return
		}
		n, err := strconv.Atoi(fields[1])
		if err != nil {
			sent, _ := SendSafeMessage(s.bot, chatID, fmt.Sprintf("❌ Nomor tidak valid: `%s`.", fields[1]), nil)
			s.sessMgr.AddTelegramMsgID(userID, sent.MessageID)
			return
		}
		// Preserve original spacing of the new prompt: strip "edit N " prefix.
		rest := strings.TrimSpace(arg[len(fields[0]):])
		rest = strings.TrimSpace(rest[len(fields[1]):])
		if !s.queueEdit(userID, n-1, rest) {
			sent, _ := SendSafeMessage(s.bot, chatID, fmt.Sprintf("❌ Item #%d tidak ada. Cek `/queue list`.", n), nil)
			s.sessMgr.AddTelegramMsgID(userID, sent.MessageID)
			return
		}
		sent, _ := SendSafeMessage(s.bot, chatID, fmt.Sprintf("✏️ *Item #%d Diperbarui:*\n`%s`", n, truncate(rest, 80)), nil)
		s.sessMgr.AddTelegramMsgID(userID, sent.MessageID)
		return
	case "move":
		if len(fields) < 3 {
			sent, _ := SendSafeMessage(s.bot, chatID, "ℹ️ Format: `/queue move A B` — pindahkan item nomor A ke posisi B.", nil)
			s.sessMgr.AddTelegramMsgID(userID, sent.MessageID)
			return
		}
		a, errA := strconv.Atoi(fields[1])
		b, errB := strconv.Atoi(fields[2])
		if errA != nil || errB != nil {
			sent, _ := SendSafeMessage(s.bot, chatID, "❌ Nomor tidak valid. Format: `/queue move A B`.", nil)
			s.sessMgr.AddTelegramMsgID(userID, sent.MessageID)
			return
		}
		if !s.queueMove(userID, a-1, b-1) {
			sent, _ := SendSafeMessage(s.bot, chatID, "❌ Nomor di luar jangkauan. Cek `/queue list`.", nil)
			s.sessMgr.AddTelegramMsgID(userID, sent.MessageID)
			return
		}
		sent, _ := SendSafeMessage(s.bot, chatID, fmt.Sprintf("🔀 *Item #%d dipindahkan ke posisi #%d.*", a, b), nil)
		s.sessMgr.AddTelegramMsgID(userID, sent.MessageID)
		return
	case "add":
		prompt := strings.TrimSpace(arg[len(fields[0]):])
		if prompt == "" {
			sent, _ := SendSafeMessage(s.bot, chatID, "ℹ️ Format: `/queue add <prompt>`.", nil)
			s.sessMgr.AddTelegramMsgID(userID, sent.MessageID)
			return
		}
		arg = prompt
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

	go s.executeTask(chatID, userID, promptToRun, lastImg, "", 1)
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

func (s *BotServer) executeTask(chatID, userID int64, prompt, imagePath, preloadSkills string, partsCount int) {
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
				s.executeTask(chatID, userID, prompt, imagePath, preloadSkills, partsCount)
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

	s.sessMgr.SetLastPrompt(userID, prompt, imagePath)

	cancelKb := CancelKeyboard()
	currentPrompt := prompt
	currentImagePath := imagePath
	maxAttempts := 2

	inactTimeout := 5 * time.Minute
	if s.cfg.Hermes.InactivityTimeoutSec > 0 {
		inactTimeout = time.Duration(s.cfg.Hermes.InactivityTimeoutSec) * time.Second
	}
	hardTimeout := 2 * time.Hour
	if s.cfg.Hermes.MaxDurationSec > 0 {
		hardTimeout = time.Duration(s.cfg.Hermes.MaxDurationSec) * time.Second
	}

	var statusMsg tgbotapi.Message
	hasStatusMsg := false

	if partsCount > 1 {
		statusText := fmt.Sprintf("🌸 *Aida sedang menjalankan tugas...*\n_📦 Menggabungkan %d potongan pesan menjadi 1 prompt utuh (%d karakter)_\n\n💭 _Sedang menganalisis instruksi..._", partsCount, len([]rune(prompt)))
		msg, err := SendSafeMessage(s.bot, chatID, statusText, cancelKb)
		if err == nil {
			statusMsg = msg
			hasStatusMsg = true
			s.sessMgr.AddTelegramMsgID(userID, statusMsg.MessageID)
		}
	} else {
		statusText := "🌸 *Aida sedang menjalankan tugas...*\n\n💭 _Sedang berpikir & menganalisis instruksi..._"
		msg, err := SendSafeMessage(s.bot, chatID, statusText, cancelKb)
		if err == nil {
			statusMsg = msg
			hasStatusMsg = true
			s.sessMgr.AddTelegramMsgID(userID, statusMsg.MessageID)
		}
	}

	for attempt := 0; attempt < maxAttempts; attempt++ {
		userSess := s.sessMgr.Get(userID, chatID)
		startTime := time.Now()

		if attempt > 0 {
			recoveryNotice := fmt.Sprintf("⚠️ *Deteksi Terhenti / Stuck — Auto-Recovery Berjalan...*\n\n_Melanjutkan pekerjaan di sesi `%s` dan menyiapkan laporan progress..._", userSess.CurrentSessionID)
			if hasStatusMsg {
				_, _ = EditSafeMessage(s.bot, chatID, statusMsg.MessageID, recoveryNotice, &cancelKb)
			} else {
				msg, err := SendSafeMessage(s.bot, chatID, recoveryNotice, cancelKb)
				if err == nil {
					statusMsg = msg
					hasStatusMsg = true
					s.sessMgr.AddTelegramMsgID(userID, statusMsg.MessageID)
				}
			}
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

		log.Printf("[bot] Task starting for User %d (Attempt: %d, Session: %s, Model: %s)",
			userID, attempt, userSess.CurrentSessionID, userSess.CurrentModel)

		// Heartbeat + self-restart approval state (per-task, guarded by mutex).
		var progMu sync.Mutex
		lastDisplay := "🌸 *Aida sedang menjalankan tugas...*\n\n💭 _Sedang berpikir & menganalisis instruksi..._"
		if attempt > 0 {
			lastDisplay = "⚠️ *Auto-Recovery Berjalan...*\n\n💭 _Menganalisis status dan melanjutkan pekerjaan..._"
		}
		lastUpdate := time.Now()
		approvalCardSent := false

		opts := engine.RunOptions{
			Prompt:            currentPrompt,
			ImagePath:         currentImagePath,
			SessionID:         userSess.CurrentSessionID,
			Model:             userSess.CurrentModel,
			ReasoningEffort:   userSess.ReasoningEffort,
			Yolo:              userSess.YoloMode,
			PreloadSkills:     preloadSkills,
			WorkingDir:        s.cfg.Hermes.WorkingDir,
			Timeout:           hardTimeout,
			InactivityTimeout: inactTimeout,
			OnProgress: func(displayText string) {
				progMu.Lock()
				lastDisplay = displayText
				lastUpdate = time.Now()
				// Self-restart always asks, even with YOLO ON (fail-closed, 60s → deny).
				needCard := !approvalCardSent && isSelfRestartCriticalText(displayText)
				if needCard {
					approvalCardSent = true
				}
				progMu.Unlock()

				if hasStatusMsg {
					_, _ = EditSafeMessage(s.bot, chatID, statusMsg.MessageID, displayText, &cancelKb)
				}
				if needCard {
					s.sendSelfRestartApprovalCard(chatID, userID, engine.ForegroundKey(userID), displayText)
				}
			},
		}

		// Heartbeat: keep editing the status message with elapsed time so it never looks stuck/dead.
		go func() {
			ticker := time.NewTicker(12 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-stopTyping:
					return
				case <-ticker.C:
					progMu.Lock()
					stale := time.Since(lastUpdate)
					base := lastDisplay
					progMu.Unlock()
					if stale < 12*time.Second || !hasStatusMsg {
						continue
					}
					elapsed := time.Since(startTime).Round(time.Second)
					hb := fmt.Sprintf("%s\n\n⏳ _Masih berjalan... (%s) — Aida aktif bekerja..._\n_Gunakan /stop untuk batalkan._", base, elapsed)
					_, _ = EditSafeMessage(s.bot, chatID, statusMsg.MessageID, hb, &cancelKb)
				}
			}
		}()

		result, runErr := s.runner.Execute(context.Background(), userID, opts)
		close(stopTyping)
		duration := time.Since(startTime).Round(time.Millisecond)

		if runErr != nil {
			log.Printf("[bot] Task error for User %d (Attempt %d, %v): %v", userID, attempt, duration, runErr)

			isStuck := errors.Is(runErr, engine.ErrInactivityTimeout) || (result != nil && result.IsStuck)

			// If stuck on attempt 0: trigger auto-recovery!
			if isStuck && attempt == 0 {
				log.Printf("[bot] Task stuck for User %d after %v inactivity. Triggering auto-recovery attempt 1...", userID, inactTimeout)
				if result != nil && result.SessionID != "" {
					s.sessMgr.SetSessionID(userID, result.SessionID)
				}
				s.withdrawSelfRestartApproval(userID, "")

				stuckNotice := fmt.Sprintf("⚠️ *Deteksi Terhenti / Stuck (%v):*\n_Tidak ada respons atau progres baru selama %v. Aida menghentikan subproses yang macet dan otomatis melanjutkan pekerjaan serta menyiapkan laporan progress..._\n\n⏳ _Menghubungi Aida untuk recovery..._", inactTimeout, inactTimeout)
				if hasStatusMsg {
					_, _ = EditSafeMessage(s.bot, chatID, statusMsg.MessageID, stuckNotice, &cancelKb)
				}
				time.Sleep(1 * time.Second)

				currentPrompt = fmt.Sprintf("[Sistem Auto-Recovery]: Perintah atau proses sebelumnya terhenti karena tidak ada respons/aktivitas selama %v (stuck). Tolong periksa kondisi sistem dan file saat ini, lanjutkan pekerjaan yang belum selesai jika memungkinkan, dan berikan laporan progress terkini serta kendala yang terjadi kepada user.", inactTimeout)
				currentImagePath = ""
				continue
			}

			// Otherwise, report final error to user
			var sb strings.Builder
			if isStuck {
				sb.WriteString(fmt.Sprintf("⏱️ *Batas Waktu Hening Terlampaui (Stuck %v)*\n\n_Operasi dihentikan otomatis karena tidak ada respons/aktivitas selama %v, dan pemulihan otomatis tidak berhasil._\n\n", inactTimeout, inactTimeout))
			} else if errors.Is(runErr, engine.ErrHardTimeout) {
				sb.WriteString(fmt.Sprintf("⏱️ *Batas Waktu Maksimal Terlampaui (Hard Ceiling %v)*\n\n_Operasi dihentikan otomatis karena mencapai batas waktu maksimal tugas._\n\n", hardTimeout))
			} else if errors.Is(runErr, engine.ErrUserCanceled) || strings.Contains(runErr.Error(), "dibatalkan") {
				sb.WriteString("🛑 *Operasi Dibatalkan oleh Pengguna*\n\n")
			} else {
				sb.WriteString(fmt.Sprintf("❌ *Gagal Mengeksekusi Tugas (%v):*\n\n```\n%v\n```\n\n", duration, runErr))
			}

			if result != nil && len(result.ToolHistory) > 0 {
				sb.WriteString("⚙️ *Aktivitas Tools Terakhir:*\n")
				start := 0
				if len(result.ToolHistory) > 6 {
					start = len(result.ToolHistory) - 6
				}
				for _, th := range result.ToolHistory[start:] {
					sb.WriteString(fmt.Sprintf("• %s\n", th))
				}
				sb.WriteString("\n")
			}

			if result != nil && strings.TrimSpace(result.FinalText) != "" {
				sb.WriteString("💬 *Catatan/Keluaran Terakhir Sebelum Terhenti:*\n")
				sb.WriteString(strings.TrimSpace(result.FinalText))
				sb.WriteString("\n")
			}

			activeSessionID := userSess.CurrentSessionID
			if result != nil && result.SessionID != "" {
				activeSessionID = result.SessionID
			}
			activeModel := userSess.CurrentModel
			if activeModel == "" && s.cfg != nil {
				activeModel = s.cfg.Hermes.DefaultModel
			}
			footer := s.formatResultFooter(duration, result, activeSessionID, activeModel)
			sb.WriteString(footer)

			errMsg := sb.String()
			chunks := SplitMessage(errMsg, 30000)
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
			s.pruneOldTelegramMessages(chatID, userID)
			s.withdrawSelfRestartApproval(userID, "")
			if !errors.Is(runErr, engine.ErrUserCanceled) && !strings.Contains(runErr.Error(), "dibatalkan") {
				if s.maybeInjectSteerFollowup(chatID, userID) {
					log.Printf("[bot] Steer follow-up queued for user %d", userID)
				}
			}
			s.checkAndRunNextQueuedTask(userID)
			return
		}

		// Task succeeded!
		if result != nil && result.SessionID != "" {
			s.sessMgr.SetSessionID(userID, result.SessionID)
		}

		finalText := ""
		if result != nil {
			finalText = strings.TrimSpace(result.FinalText)
		}

		if finalText == "" {
			finalText = fmt.Sprintf("✅ *Tugas selesai dalam %v tanpa keluaran teks.*", duration)
		} else if attempt > 0 {
			finalText = "🔄 *[Auto-Recovery Sukses]*\n_Aida berhasil melanjutkan tugas setelah sempat terhenti:_\n\n" + finalText
		}

		// Intercept and send any outbound files/images/videos directly to Telegram!
		finalText = s.ProcessOutboundMedia(chatID, userID, finalText)

		activeSessionID := userSess.CurrentSessionID
		if result != nil && result.SessionID != "" {
			activeSessionID = result.SessionID
		}
		activeModel := userSess.CurrentModel
		if activeModel == "" && s.cfg != nil {
			activeModel = s.cfg.Hermes.DefaultModel
		}

		footer := s.formatResultFooter(duration, result, activeSessionID, activeModel)
		fullResponse := finalText + footer

		chunks := SplitMessage(fullResponse, 30000)
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

		log.Printf("[bot] Task completed successfully in %v (Session: %s, Chunks: %d, Attempt: %d)",
			duration, userSess.CurrentSessionID, len(chunks), attempt)

		s.pruneOldTelegramMessages(chatID, userID)
		s.withdrawSelfRestartApproval(userID, "")

		if s.maybeInjectSteerFollowup(chatID, userID) {
			log.Printf("[bot] Steer follow-up queued for user %d", userID)
		}

		s.checkAndRunNextQueuedTask(userID)
		return
	}
}

func (s *BotServer) checkAndRunNextQueuedTask(userID int64) {
	if next := s.popNextTask(userID); next != nil {
		log.Printf("[bot] Running next queued task for User %d: %q", userID, next.Prompt)
		go s.executeTask(next.ChatID, next.UserID, next.Prompt, next.ImagePath, next.PreloadSkills, 1)
	}
}

func (s *BotServer) pruneOldTelegramMessages(chatID, userID int64) {
	// Auto-prune turns when threshold (default: 50 turns) is reached, pruning down to 25 turns
	toDelete := s.sessMgr.PruneTurns(userID)
	if len(toDelete) == 0 {
		return
	}

	go func(chat int64, ids []int) {
		for _, id := range ids {
			del := tgbotapi.NewDeleteMessage(chat, id)
			_, _ = s.bot.Send(del)
			time.Sleep(30 * time.Millisecond)
		}
		log.Printf("[bot] Auto-pruned %d old Telegram messages in chat %d (maintained 25 turns window)", len(ids), chat)
	}(chatID, toDelete)
}

func truncate(s string, max int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > max {
		return s[:max] + "..."
	}
	return s
}

// isSelfRestartRiskText detects self-restart patterns in the live tool-activity text.
// Runs on the gateway side only for warning purposes — execution is NOT blocked per user request.
func isSelfRestartRiskText(displayText string) bool {
	lower := strings.ToLower(displayText)
	hasSystemctl := strings.Contains(lower, "systemctl")
	hasHermesTele := strings.Contains(lower, "hermes-tele")
	hasGoBuild := strings.Contains(lower, "go build")
	hasAppsHermes := strings.Contains(lower, "apps/hermes-tele")

	// systemctl ... hermes-tele (restart/stop/start) from inside its own task = self-kill deadlock
	if hasSystemctl && hasHermesTele {
		return true
	}
	// go build inside the bot folder from inside its own task = binary replaced mid-run
	if hasGoBuild && (hasHermesTele || hasAppsHermes) {
		return true
	}
	return false
}

func (s *BotServer) formatResultFooter(duration time.Duration, result *engine.RunResult, sessionID string, model string) string {
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
			if model == "" && details.Model != "" {
				model = details.Model
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

	// 4. Model (inserted before version)
	if model == "" && s.cfg != nil {
		model = s.cfg.Hermes.DefaultModel
	}
	if model != "" {
		parts = append(parts, fmt.Sprintf("🤖 %s", model))
	}

	// 5. Version
	ver := s.version
	if !strings.HasPrefix(ver, "v") {
		ver = "v" + ver
	}
	parts = append(parts, fmt.Sprintf("🏷️ %s", ver))

	return "\n\n_(" + strings.Join(parts, " • ") + ")_"
}

