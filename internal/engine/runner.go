package engine

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

type RunOptions struct {
	Prompt          string
	ImagePath       string
	SessionID       string
	Model           string
	ReasoningEffort string
	Yolo            bool
	PreloadSkills   string
	WorkingDir      string
	OnProgress      func(displayText string)
}

type Runner struct {
	binaryPath string
	mu         sync.Mutex
	running    map[int64]context.CancelFunc
}

func NewRunner(binaryPath string) *Runner {
	return &Runner{
		binaryPath: binaryPath,
		running:    make(map[int64]context.CancelFunc),
	}
}

func (r *Runner) IsRunning(userID int64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, exists := r.running[userID]
	return exists
}

func (r *Runner) Stop(userID int64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	cancel, exists := r.running[userID]
	if exists {
		cancel()
		delete(r.running, userID)
		return true
	}
	return false
}

func (r *Runner) Execute(ctx context.Context, userID int64, opts RunOptions) (*RunResult, error) {
	r.mu.Lock()
	if _, busy := r.running[userID]; busy {
		r.mu.Unlock()
		return nil, fmt.Errorf("ada tugas yang masih berjalan untuk pengguna ini")
	}

	runCtx, cancel := context.WithCancel(ctx)
	r.running[userID] = cancel
	r.mu.Unlock()

	defer func() {
		r.mu.Lock()
		delete(r.running, userID)
		r.mu.Unlock()
		cancel()
	}()

	// Write prompt to a temporary file for 100% shell-safe query passing
	tmpFile, err := os.CreateTemp("", "hermes_query_*.txt")
	if err != nil {
		return nil, fmt.Errorf("gagal membuat query temp file: %w", err)
	}
	tmpFilePath := tmpFile.Name()
	defer os.Remove(tmpFilePath)

	if _, err := tmpFile.WriteString(opts.Prompt); err != nil {
		_ = tmpFile.Close()
		return nil, fmt.Errorf("gagal menulis ke query temp file: %w", err)
	}
	_ = tmpFile.Close()

	// Build arguments
	args := []string{
		"chat",
		"--query-file", tmpFilePath,
		"--format", "stream-json",
	}

	if opts.SessionID != "" {
		args = append(args, "--resume", opts.SessionID)
	}
	if opts.Model != "" {
		args = append(args, "-m", opts.Model)
	}
	if opts.ReasoningEffort != "" {
		args = append(args, "--reasoning", opts.ReasoningEffort)
	}
	if opts.Yolo {
		args = append(args, "--yolo")
	}
	if opts.ImagePath != "" {
		args = append(args, "--image", opts.ImagePath)
	}
	if opts.PreloadSkills != "" {
		args = append(args, "-s", opts.PreloadSkills)
	}
	if opts.WorkingDir != "" {
		args = append(args, "--in", opts.WorkingDir)
	}

	log.Printf("[engine] Executing hermes for user %d (session=%s, model=%s)", userID, opts.SessionID, opts.Model)

	cmd := exec.CommandContext(runCtx, r.binaryPath, args...)
	if opts.WorkingDir != "" {
		cmd.Dir = opts.WorkingDir
	}

	// Crucial: Set PYTHONUNBUFFERED=1 to stream tokens immediately without buffering!
	cmd.Env = append(os.Environ(), "PYTHONUNBUFFERED=1")

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe error: %w", err)
	}

	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start hermes: %w", err)
	}

	scanner := bufio.NewScanner(stdoutPipe)
	buf := make([]byte, 64*1024)
	scanner.Buffer(buf, 2*1024*1024)

	var (
		sessionID       = opts.SessionID
		accumulatedText strings.Builder
		currentToolFull string
		toolHistory     []string
		lastProgress    time.Time
		resultObj       *RunResult
		isAnswering     bool
	)

	triggerProgress := func(force bool) {
		if opts.OnProgress == nil {
			return
		}
		now := time.Now()
		if !force && now.Sub(lastProgress) < 1500*time.Millisecond {
			return
		}
		lastProgress = now

		var sb strings.Builder
		sb.WriteString("🌸 *Aida sedang menjalankan tugas...*\n\n")

		// Display tool execution history
		if len(toolHistory) > 0 {
			sb.WriteString("⚙️ *Aktivitas Tools:*\n")
			start := 0
			if len(toolHistory) > 5 {
				start = len(toolHistory) - 5
			}
			for _, th := range toolHistory[start:] {
				sb.WriteString(fmt.Sprintf("• ✓ %s\n", th))
			}
			sb.WriteString("\n")
		}

		// Display currently running tool or phase
		if currentToolFull != "" {
			sb.WriteString(fmt.Sprintf("⚡ *Sedang dijalankan:* %s\n⏳ _Menunggu proses selesai..._\n\n", currentToolFull))
		} else if isAnswering {
			sb.WriteString("⚡ *Status:* ✍️ _Menyusun dan merapikan jawaban untuk kamu..._\n\n")
		} else if len(toolHistory) == 0 {
			sb.WriteString("💭 _Sedang berpikir & menganalisis instruksi..._\n\n")
		}

		opts.OnProgress(sb.String())
	}

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}

		event, err := ParseEvent(line)
		if err != nil {
			continue
		}

		switch event.Type {
		case EventSystem:
			if event.SessionID != "" {
				sessionID = event.SessionID
			}
		case EventText:
			if event.Text != "" {
				accumulatedText.WriteString(event.Text)
				isAnswering = true
				triggerProgress(false)
			}
		case EventToolUse:
			icon, desc, detail := FormatToolActivity(event.Name, event.Input)
			detailStr := ""
			if detail != "" {
				detailStr = fmt.Sprintf(": `%s`", detail)
			}
			currentToolFull = fmt.Sprintf("%s *%s*%s", icon, desc, detailStr)
			isAnswering = false
			triggerProgress(true)
		case EventToolResult:
			durStr := ""
			if event.DurationMs >= 1000 {
				durStr = fmt.Sprintf("%.1fs", float64(event.DurationMs)/1000.0)
			} else if event.DurationMs > 0 {
				durStr = fmt.Sprintf("%dms", event.DurationMs)
			}

			durLabel := ""
			if durStr != "" {
				durLabel = fmt.Sprintf(" `(%s)`", durStr)
			}

			if currentToolFull != "" {
				toolHistory = append(toolHistory, currentToolFull+durLabel)
			} else {
				toolHistory = append(toolHistory, fmt.Sprintf("🔧 `%s`%s", event.Name, durLabel))
			}
			currentToolFull = ""
			isAnswering = false
			triggerProgress(true)
		case EventResult:
			tokens := TokenUsage{}
			if event.Tokens != nil {
				tokens = *event.Tokens
			}
			resultObj = &RunResult{
				SessionID:   event.SessionID,
				FinalText:   event.Text,
				Tokens:      tokens,
				DurationMs:  event.DurationMs,
				ExitCode:    event.ExitCode,
				Error:       event.Error,
				ToolHistory: toolHistory,
			}
			if resultObj.SessionID == "" {
				resultObj.SessionID = sessionID
			}
		}
	}

	cmdErr := cmd.Wait()

	if runCtx.Err() == context.Canceled {
		return nil, fmt.Errorf("operasi dibatalkan oleh pengguna")
	}

	if resultObj == nil {
		finalStr := accumulatedText.String()
		stderrStr := strings.TrimSpace(stderrBuf.String())

		if cmdErr != nil && finalStr == "" {
			return nil, fmt.Errorf("hermes error (%v): %s", cmdErr, stderrStr)
		}

		resultObj = &RunResult{
			SessionID:   sessionID,
			FinalText:   finalStr,
			ExitCode:    0,
			ToolHistory: toolHistory,
		}
	}

	if resultObj.FinalText == "" && accumulatedText.Len() > 0 {
		resultObj.FinalText = accumulatedText.String()
	}
	if len(resultObj.ToolHistory) == 0 && len(toolHistory) > 0 {
		resultObj.ToolHistory = toolHistory
	}

	return resultObj, nil
}

func FormatToolActivity(name string, input map[string]interface{}) (icon, desc, detail string) {
	switch name {
	case "terminal":
		cmd := ""
		if c, ok := input["command"].(string); ok {
			cmd = truncate(c, 45)
		}
		return "💻", "Bash / Terminal", cmd
	case "browser_navigate":
		url := ""
		if u, ok := input["url"].(string); ok {
			url = truncate(u, 50)
		}
		return "🌐", "Membuka Web (Camoufox)", url
	case "browser_snapshot":
		return "📸", "Membaca Tampilan Web (Camoufox)", ""
	case "browser_click":
		elem := ""
		if r, ok := input["ref"].(string); ok {
			elem = r
		} else if e, ok := input["element"].(string); ok {
			elem = truncate(e, 35)
		}
		return "🖱️", "Klik Elemen (Camoufox)", elem
	case "browser_type":
		txt := ""
		if t, ok := input["text"].(string); ok {
			txt = truncate(t, 30)
		}
		return "⌨️", "Ketik Form (Camoufox)", txt
	case "browser_vision":
		return "👁️", "Analisis Visual (Camoufox Vision)", ""
	case "browser_scroll":
		return "📜", "Scroll Halaman (Camoufox)", ""
	case "browser_screenshot":
		return "🖼️", "Screenshot Layar (Camoufox)", ""
	case "browser_console":
		expr := ""
		if e, ok := input["expression"].(string); ok {
			expr = truncate(e, 35)
		}
		return "🖥️", "Console JS (Camoufox)", expr
	case "browser_tabs":
		return "📑", "Kelola Tab (Camoufox)", ""
	case "browser_close":
		return "🚪", "Tutup Tab (Camoufox)", ""
	case "web_search":
		query := ""
		if q, ok := input["query"].(string); ok {
			query = truncate(q, 45)
		}
		return "🔍", "Pencarian Web", query
	case "web_extract":
		url := ""
		if u, ok := input["url"].(string); ok {
			url = truncate(u, 50)
		}
		return "📄", "Ekstrak Konten Web", url
	case "skill_view":
		skill := ""
		if s, ok := input["name"].(string); ok {
			skill = s
		}
		return "📖", "Memuat Panduan Skill", skill
	case "read_file":
		path := ""
		if p, ok := input["path"].(string); ok {
			path = truncate(p, 45)
		}
		return "📂", "Membaca File", path
	case "write_file":
		path := ""
		if p, ok := input["path"].(string); ok {
			path = truncate(p, 45)
		}
		return "💾", "Menulis File", path
	default:
		detail := ""
		for _, k := range []string{"command", "query", "url", "path", "file", "target"} {
			if v, ok := input[k].(string); ok {
				detail = truncate(v, 40)
				break
			}
		}
		return "🔧", name, detail
	}
}

func truncate(s string, max int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > max {
		return s[:max] + "..."
	}
	return s
}

