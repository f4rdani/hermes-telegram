package session

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAutoDelete50To25Turns(t *testing.T) {
	tmpDir := t.TempDir()
	storagePath := filepath.Join(tmpDir, "sessions.json")
	mgr := NewManager("Top", storagePath, filepath.Join(tmpDir, "state.db"), "/usr/local/bin/hermes")

	userID := int64(12345)
	chatID := int64(12345)
	_ = mgr.Get(userID, chatID)

	// Simulate 49 turns
	msgIDCounter := 1000
	for i := 1; i <= 49; i++ {
		msgIDCounter++
		mgr.StartTurn(userID, msgIDCounter)
		msgIDCounter++
		mgr.AddTelegramMsgID(userID, msgIDCounter) // bot reply
		toDel := mgr.PruneTurns(userID)
		if len(toDel) > 0 {
			t.Fatalf("Turn %d should not trigger prune, got %d to delete", i, len(toDel))
		}
	}

	maxTurns, targetKeep, curTurns, curMsgs, enabled := mgr.GetAutoDeleteStats(userID)
	if !enabled || maxTurns != 50 || targetKeep != 25 {
		t.Fatalf("Unexpected stats: enabled=%v, max=%d, keep=%d", enabled, maxTurns, targetKeep)
	}
	if curTurns != 49 {
		t.Fatalf("Expected 49 turns, got %d", curTurns)
	}
	if curMsgs != 49*2 {
		t.Fatalf("Expected %d messages, got %d", 49*2, curMsgs)
	}

	// Now add 50th turn (should trigger pruning down to 25 turns!)
	msgIDCounter++
	mgr.StartTurn(userID, msgIDCounter)
	msgIDCounter++
	mgr.AddTelegramMsgID(userID, msgIDCounter)

	toDel := mgr.PruneTurns(userID)
	// 50 turns minus 25 turns = 25 oldest turns pruned = 50 messages
	if len(toDel) != 50 {
		t.Fatalf("Expected 50 messages to delete (25 turns), got %d: %v", len(toDel), toDel)
	}

	// Verify remaining turns is 25
	_, _, curTurnsAfter, curMsgsAfter, _ := mgr.GetAutoDeleteStats(userID)
	if curTurnsAfter != 25 {
		t.Fatalf("Expected 25 turns remaining, got %d", curTurnsAfter)
	}
	if curMsgsAfter != 50 {
		t.Fatalf("Expected 50 messages remaining, got %d", curMsgsAfter)
	}

	// Test persistence across restart
	mgr.SaveSync()
	mgr2 := NewManager("Top", storagePath, filepath.Join(tmpDir, "state.db"), "/usr/local/bin/hermes")
	_, _, curTurnsLoaded, curMsgsLoaded, _ := mgr2.GetAutoDeleteStats(userID)
	if curTurnsLoaded != 25 || curMsgsLoaded != 50 {
		t.Fatalf("State not restored correctly: turns=%d, msgs=%d", curTurnsLoaded, curMsgsLoaded)
	}
}

func TestLegacyMigration(t *testing.T) {
	tmpDir := t.TempDir()
	storagePath := filepath.Join(tmpDir, "sessions.json")

	// Write mock legacy sessions.json with 100 recent_telegram_msg_ids
	legacyJSON := `{
		"999": {
			"user_id": 999,
			"chat_id": 999,
			"current_model": "Top",
			"recent_telegram_msg_ids": [1, 2, 3, 4, 5, 6, 7, 8, 9, 10]
		}
	}`
	if err := os.WriteFile(storagePath, []byte(legacyJSON), 0644); err != nil {
		t.Fatal(err)
	}

	mgr := NewManager("Top", storagePath, filepath.Join(tmpDir, "state.db"), "/usr/local/bin/hermes")
	_, _, curTurns, curMsgs, _ := mgr.GetAutoDeleteStats(999)
	if curTurns != 5 {
		t.Fatalf("Expected 5 turns migrated from 10 msgs, got %d", curTurns)
	}
	if curMsgs != 10 {
		t.Fatalf("Expected 10 msgs, got %d", curMsgs)
	}
}
