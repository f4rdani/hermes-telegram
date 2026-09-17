package session

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

type UserSession struct {
	UserID           int64     `json:"user_id"`
	ChatID           int64     `json:"chat_id"`
	CurrentSessionID string    `json:"current_session_id"`
	CurrentModel     string    `json:"current_model"`
	ReasoningEffort      string    `json:"reasoning_effort"`
	YoloMode             bool      `json:"yolo_mode"`
	BusyMode             string    `json:"busy_mode"`
	LastActivity         time.Time `json:"last_activity"`
	LastPrompt           string    `json:"last_prompt,omitempty"`
	LastImagePath        string    `json:"last_image_path,omitempty"`
	RecentTelegramMsgIDs []int     `json:"recent_telegram_msg_ids,omitempty"`
}

type SessionSummary struct {
	ID           string
	Title        string
	Model        string
	MessageCount int
	InputTokens  int
	OutputTokens int
	LastActivity time.Time
}

type Manager struct {
	mu           sync.RWMutex
	sessions     map[int64]*UserSession
	defaultModel string
	storagePath  string
	stateDBPath  string
	hermesBin    string
}

func NewManager(defaultModel, storagePath, stateDBPath, hermesBin string) *Manager {
	m := &Manager{
		sessions:     make(map[int64]*UserSession),
		defaultModel: defaultModel,
		storagePath:  storagePath,
		stateDBPath:  stateDBPath,
		hermesBin:    hermesBin,
	}
	m.loadState()
	return m
}

func (m *Manager) Get(userID int64, chatID int64) *UserSession {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[userID]
	if !ok {
		s = &UserSession{
			UserID:          userID,
			ChatID:          chatID,
			CurrentModel:    m.defaultModel,
			ReasoningEffort: "high",
			YoloMode:        true,
			BusyMode:        "queue",
			LastActivity:    time.Now(),
		}
		m.sessions[userID] = s
		go m.saveState()
	}
	if s.BusyMode == "" {
		s.BusyMode = "queue"
	}
	s.ChatID = chatID
	s.LastActivity = time.Now()
	return s
}

func (m *Manager) SetSessionID(userID int64, sessionID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.sessions[userID]; ok {
		s.CurrentSessionID = sessionID
		s.LastActivity = time.Now()
		go m.saveState()
	}
}

func (m *Manager) SetModel(userID int64, model string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.sessions[userID]; ok {
		s.CurrentModel = model
		s.LastActivity = time.Now()
		go m.saveState()
	}
}

func (m *Manager) SetReasoning(userID int64, effort string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.sessions[userID]; ok {
		s.ReasoningEffort = effort
		s.LastActivity = time.Now()
		go m.saveState()
	}
}

func (m *Manager) ToggleYolo(userID int64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.sessions[userID]; ok {
		s.YoloMode = !s.YoloMode
		s.LastActivity = time.Now()
		go m.saveState()
		return s.YoloMode
	}
	return false
}

func (m *Manager) SetBusyMode(userID int64, mode string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.sessions[userID]; ok {
		s.BusyMode = mode
		s.LastActivity = time.Now()
		go m.saveState()
	}
}

func (m *Manager) ResetSession(userID int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.sessions[userID]; ok {
		s.CurrentSessionID = ""
		s.LastPrompt = ""
		s.LastImagePath = ""
		s.LastActivity = time.Now()
		go m.saveState()
	}
}

func (m *Manager) SetLastPrompt(userID int64, prompt, imagePath string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.sessions[userID]; ok {
		s.LastPrompt = prompt
		s.LastImagePath = imagePath
		s.LastActivity = time.Now()
		go m.saveState()
	}
}

func (m *Manager) GetLastPrompt(userID int64, sessionID string) (string, string) {
	m.mu.RLock()
	s, ok := m.sessions[userID]
	if ok && s.LastPrompt != "" {
		p := s.LastPrompt
		img := s.LastImagePath
		m.mu.RUnlock()
		return p, img
	}
	m.mu.RUnlock()

	if sessionID == "" {
		return "", ""
	}

	// Fallback to SQLite state.db if bot process was restarted
	query := fmt.Sprintf(
		"SELECT content FROM messages WHERE session_id = '%s' AND role = 'user' ORDER BY id DESC LIMIT 1;",
		strings.ReplaceAll(sessionID, "'", "''"),
	)
	cmd := exec.Command("sqlite3", m.stateDBPath, query)
	out, err := cmd.Output()
	if err != nil {
		return "", ""
	}
	return strings.TrimSpace(string(out)), ""
}

func (m *Manager) UndoLastTurn(sessionID string) error {
	if sessionID == "" {
		return fmt.Errorf("tidak ada sesi aktif")
	}
	safeSessionID := strings.ReplaceAll(sessionID, "'", "''")

	query := fmt.Sprintf(
		"DELETE FROM messages WHERE session_id = '%s' AND id >= (SELECT COALESCE(MAX(id), 0) FROM messages WHERE session_id = '%s' AND role = 'user'); "+
			"UPDATE sessions SET message_count = (SELECT COUNT(*) FROM messages WHERE session_id = '%s') WHERE id = '%s';",
		safeSessionID, safeSessionID, safeSessionID, safeSessionID,
	)
	cmd := exec.Command("sqlite3", m.stateDBPath, query)
	return cmd.Run()
}

func (m *Manager) AddTelegramMsgID(userID int64, msgID int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.sessions[userID]; ok {
		s.RecentTelegramMsgIDs = append(s.RecentTelegramMsgIDs, msgID)
		go m.saveState()
	}
}

func (m *Manager) PopOldTelegramMsgIDs(userID int64, maxCount int) []int {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[userID]
	if !ok || len(s.RecentTelegramMsgIDs) <= maxCount {
		return nil
	}
	excess := len(s.RecentTelegramMsgIDs) - maxCount
	toDelete := make([]int, excess)
	copy(toDelete, s.RecentTelegramMsgIDs[:excess])
	s.RecentTelegramMsgIDs = s.RecentTelegramMsgIDs[excess:]
	go m.saveState()
	return toDelete
}

func (m *Manager) ClearTelegramMsgIDs(userID int64) []int {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[userID]
	if !ok || len(s.RecentTelegramMsgIDs) == 0 {
		return nil
	}
	ids := append([]int(nil), s.RecentTelegramMsgIDs...)
	s.RecentTelegramMsgIDs = nil
	go m.saveState()
	return ids
}

func (m *Manager) loadState() {
	m.mu.Lock()
	defer m.mu.Unlock()

	data, err := os.ReadFile(m.storagePath)
	if err != nil {
		return
	}
	var stored map[int64]*UserSession
	if err := json.Unmarshal(data, &stored); err == nil {
		m.sessions = stored
		log.Printf("[session] Loaded %d user sessions from %s", len(m.sessions), m.storagePath)
	}
}

func (m *Manager) saveState() {
	m.mu.RLock()
	data, err := json.MarshalIndent(m.sessions, "", "  ")
	m.mu.RUnlock()
	if err != nil {
		return
	}
	_ = os.WriteFile(m.storagePath, data, 0644)
}

func (m *Manager) ListHermesSessions(limit int) ([]SessionSummary, error) {
	if limit <= 0 {
		limit = 8
	}
	query := fmt.Sprintf(
		"SELECT id, COALESCE(title, ''), COALESCE(model, ''), message_count, input_tokens, output_tokens, COALESCE(last_activity_at, started_at) FROM sessions ORDER BY COALESCE(last_activity_at, started_at) DESC LIMIT %d;",
		limit,
	)

	cmd := exec.Command("sqlite3", m.stateDBPath, query)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("sqlite3 query error: %w", err)
	}

	var results []SessionSummary
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.Split(line, "|")
		if len(parts) < 7 {
			continue
		}

		msgCount, _ := strconv.Atoi(parts[3])
		inTokens, _ := strconv.Atoi(parts[4])
		outTokens, _ := strconv.Atoi(parts[5])
		secFloat, _ := strconv.ParseFloat(parts[6], 64)
		t := time.Unix(int64(secFloat), 0)

		title := parts[1]
		if title == "" {
			title = "(tanpa judul)"
		}

		results = append(results, SessionSummary{
			ID:           parts[0],
			Title:        title,
			Model:        parts[2],
			MessageCount: msgCount,
			InputTokens:  inTokens,
			OutputTokens: outTokens,
			LastActivity: t,
		})
	}

	return results, nil
}

func (m *Manager) GetSessionDetails(sessionID string) (*SessionSummary, error) {
	query := fmt.Sprintf(
		"SELECT id, COALESCE(title, ''), COALESCE(model, ''), message_count, input_tokens, output_tokens, COALESCE(last_activity_at, started_at) FROM sessions WHERE id = '%s' LIMIT 1;",
		strings.ReplaceAll(sessionID, "'", "''"),
	)
	cmd := exec.Command("sqlite3", m.stateDBPath, query)
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	line := strings.TrimSpace(string(out))
	if line == "" {
		return nil, fmt.Errorf("session %s not found", sessionID)
	}
	parts := strings.Split(line, "|")
	if len(parts) < 7 {
		return nil, fmt.Errorf("invalid session row")
	}
	msgCount, _ := strconv.Atoi(parts[3])
	inTokens, _ := strconv.Atoi(parts[4])
	outTokens, _ := strconv.Atoi(parts[5])
	secFloat, _ := strconv.ParseFloat(parts[6], 64)

	return &SessionSummary{
		ID:           parts[0],
		Title:        parts[1],
		Model:        parts[2],
		MessageCount: msgCount,
		InputTokens:  inTokens,
		OutputTokens: outTokens,
		LastActivity: time.Unix(int64(secFloat), 0),
	}, nil
}

func (m *Manager) RenameSession(sessionID, title string) error {
	cmd := exec.Command(m.hermesBin, "sessions", "rename", sessionID, title)
	return cmd.Run()
}
