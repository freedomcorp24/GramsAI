// internal/memory/extract.go
//
// Phase 1 extraction: turn conversations into durable facts.
//
// Snapshot (hot path): the proxy calls SnapshotConversation on each request with
//   the messages array. We token-cap it (~15k) and store it encrypted in Redis,
//   overwriting (each request carries the full convo, so newest snapshot wins).
//   Fail-open, ~1ms.
//
// Worker (background): StartExtractor sweeps users on an interval. For each user
//   with a snapshot + DEK present + >24h since last extraction, it sends the
//   conversation to flash with a high-bar prompt, conflict-resolves the returned
//   facts (ADD/UPDATE/NOOP), stores them encrypted, marks extracted, clears the
//   snapshot. One flash call per user per 24h, active-session only.
package memory

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

const (
	snapPrefix      = "memsnap:" // memsnap:<user_id> -> base64(AES(messages-json))
	maxSnapTokens   = 15000      // approx token cap on the conversation we keep
	extractInterval = 5 * time.Minute
	extractEvery    = 24 * time.Hour // min gap between extractions per user
	factCap         = 150           // per-user live fact budget
	flashModel      = "deepseek/deepseek-v4-flash-20260423"
)

// approxTokens ~ chars/4.
func approxTokens(s string) int { return len(s) / 4 }

// SnapshotConversation stores a token-capped, encrypted copy of the conversation
// for later extraction. Called from the proxy (fail-open: errors are ignored).
func (s *Store) SnapshotConversation(ctx context.Context, userID int64, messages []interface{}) {
	if s.rdb == nil || len(messages) == 0 {
		return
	}
	dek, err := s.keys.Get(ctx, userID)
	if err != nil {
		return // no DEK -> can't encrypt -> skip
	}
	// keep most-recent messages within the token cap (walk from the end)
	var kept []interface{}
	total := 0
	for i := len(messages) - 1; i >= 0; i-- {
		mm, _ := json.Marshal(messages[i])
		t := approxTokens(string(mm))
		if total+t > maxSnapTokens && len(kept) > 0 {
			break
		}
		total += t
		kept = append([]interface{}{messages[i]}, kept...)
	}
	raw, err := json.Marshal(kept)
	if err != nil {
		return
	}
	blob, err := EncryptContent(dek, raw)
	if err != nil {
		return
	}
	_ = s.rdb.Set(ctx, snapPrefix+itoa(userID), base64.StdEncoding.EncodeToString(blob), 30*24*time.Hour).Err()
}

// loadSnapshot decrypts a user's conversation snapshot.
func (s *Store) loadSnapshot(ctx context.Context, userID int64, dek []byte) ([]interface{}, bool) {
	v, err := s.rdb.Get(ctx, snapPrefix+itoa(userID)).Result()
	if err != nil {
		return nil, false
	}
	blob, err := base64.StdEncoding.DecodeString(v)
	if err != nil {
		return nil, false
	}
	raw, err := DecryptContent(dek, blob)
	if err != nil {
		return nil, false
	}
	var msgs []interface{}
	if json.Unmarshal(raw, &msgs) != nil {
		return nil, false
	}
	return msgs, true
}

// extractedFact is the JSON shape flash returns.
type extractedFact struct {
	Category   string  `json:"category"`
	Fact       string  `json:"fact"`
	Confidence float32 `json:"confidence"`
}

const extractPrompt = `You maintain a long-term profile of this user to help future conversations.
Read the conversation and extract ONLY durable, important facts worth remembering permanently.

INCLUDE: identity (name, location, languages), stable preferences (how they communicate, tools/tech they use), ongoing projects and goals, key relationships, technical environment.
EXCLUDE: one-off questions, transient task details, anything tied only to today's request, anything speculative.

Rules:
- Prefer one dense fact over several thin ones. Summarize.
- Only output facts the user would expect you to remember next time.
- If nothing durable was shared, return an empty array. Most chats add nothing — that is correct.

Also capture PROCEDURE knowledge — the user's technical environment and how they work: tech stack and tools, build/deploy/test commands, project structure, conventions, and workflows they follow. These help on future technical tasks. Mark these with category "procedure".

Return JSON ONLY, no prose, no markdown fences:
[{"category":"identity|preference|project|relationship|tech_context|procedure|other","fact":"<concise durable fact or procedure>","confidence":0.0-1.0}]` // GRAMSAI_PROCEDURE

// StartExtractor launches the background extraction worker.
func (s *Store) StartExtractor(orKey string) {
	go func() {
		t := time.NewTicker(extractInterval)
		defer t.Stop()
		for range t.C {
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
			s.runExtractionPass(ctx, orKey)
			cancel()
		}
	}()
}

// runExtractionPass finds eligible users and extracts for each.
func (s *Store) runExtractionPass(ctx context.Context, orKey string) {
	// eligible = memory enabled, active session, >24h since last extraction.
	rows, err := s.pool.Query(ctx, `
		SELECT u.id FROM users u
		WHERE u.memory_enabled = true
		  AND (u.memory_extracted_at IS NULL OR u.memory_extracted_at < now() - interval '24 hours')
		  AND EXISTS (SELECT 1 FROM sessions sx WHERE sx.user_id=u.id AND sx.expires_at > now())`)
	if err != nil {
		return
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	for _, uid := range ids {
		dek, err := s.keys.Get(ctx, uid)
		if err != nil {
			continue
		}
		msgs, ok := s.loadSnapshot(ctx, uid, dek)
		if !ok || len(msgs) == 0 {
			continue
		}
		s.extractForUser(ctx, uid, msgs, orKey)
		// mark extracted regardless of how many facts (rate-limit holds even on empty)
		_, _ = s.pool.Exec(ctx, `UPDATE users SET memory_extracted_at = now() WHERE id=$1`, uid)
		// clear the snapshot so we don't re-extract the same convo
		_ = s.rdb.Del(ctx, snapPrefix+itoa(uid)).Err()
	}
}

// extractForUser runs one flash extraction + conflict-resolves + stores.
func (s *Store) extractForUser(ctx context.Context, uid int64, msgs []interface{}, orKey string) {
	convo := messagesToText(msgs)
	if strings.TrimSpace(convo) == "" {
		return
	}
	facts, err := callFlashExtract(ctx, orKey, convo)
	if err != nil {
		log.Printf("extract: user %d flash ERROR: %v", uid, err)
		return
	}
	if len(facts) == 0 {
		return
	}
	// load existing facts once for simple dedup (case-insensitive contains/equality).
	existing, _ := s.LoadMemories(ctx, uid, "fact")
	count := len(existing)

	for _, f := range facts {
		f.Fact = strings.TrimSpace(f.Fact)
		if f.Fact == "" {
			continue
		}
		if f.Confidence <= 0 {
			f.Confidence = 0.8
		}
		// NOOP if a near-identical fact already exists.
		dup := false
		for _, e := range existing {
			if strings.EqualFold(strings.TrimSpace(e.Content), f.Fact) {
				dup = true
				break
			}
		}
		if dup {
			continue
		}
		if count >= factCap {
			// budget reached: skip new low-value adds. (Consolidation pass = later.)
			if f.Confidence < 0.9 {
				continue
			}
		}
		// GRAMSAI_PROCEDURE: route procedure items to their own type so injection
		// can target technical specialties only.
		memType := "fact"
		cat := normCategory(f.Category)
		if strings.EqualFold(strings.TrimSpace(f.Category), "procedure") {
			memType = "procedure"
			cat = "procedure"
		}
		if err := s.StoreMemory(ctx, uid, memType, cat, f.Fact, f.Confidence, "extract"); err == nil {
			count++
		}
	}
	log.Printf("extract: user %d stored facts (now %d live)", uid, count)
}

func normCategory(c string) string {
	c = strings.ToLower(strings.TrimSpace(c))
	switch c {
	case "identity", "preference", "project", "relationship", "tech_context", "procedure":
		return c
	default:
		return "other"
	}
}

// messagesToText flattens the OpenAI-style messages array into role: text lines.
func messagesToText(msgs []interface{}) string {
	var sb strings.Builder
	for _, m := range msgs {
		mm, ok := m.(map[string]interface{})
		if !ok {
			continue
		}
		role, _ := mm["role"].(string)
		if role == "system" {
			continue // skip injected system/memory blocks
		}
		content, _ := mm["content"].(string)
		if content == "" {
			continue
		}
		sb.WriteString(role)
		sb.WriteString(": ")
		sb.WriteString(content)
		sb.WriteString("\n")
	}
	return sb.String()
}

// callFlashExtract calls OpenRouter (flash) and parses the JSON fact array.
func callFlashExtract(ctx context.Context, orKey, convo string) ([]extractedFact, error) {
	reqBody := map[string]interface{}{
		"model":  flashModel,
		"stream": false,
		"messages": []map[string]string{
			{"role": "system", "content": extractPrompt},
			{"role": "user", "content": "Conversation:\n" + convo},
		},
		"provider": map[string]interface{}{"order": []string{"DeepSeek"}, "require_parameters": true},
	}
	jb, _ := json.Marshal(reqBody)
	req, _ := http.NewRequestWithContext(ctx, "POST", "https://openrouter.ai/api/v1/chat/completions", bytes.NewReader(jb))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+orKey)
	req.Header.Set("HTTP-Referer", "https://grams.chat")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)

	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil || len(parsed.Choices) == 0 {
		return nil, fmt.Errorf("bad extract response")
	}
	content := strings.TrimSpace(parsed.Choices[0].Message.Content)
	// strip accidental markdown fences
	content = strings.TrimPrefix(content, "```json")
	content = strings.TrimPrefix(content, "```")
	content = strings.TrimSuffix(content, "```")
	content = strings.TrimSpace(content)

	var facts []extractedFact
	if err := json.Unmarshal([]byte(content), &facts); err != nil {
		return nil, fmt.Errorf("fact JSON parse: %w", err)
	}
	return facts, nil
}


const episodePrompt = `Summarize this conversation into ONE dense paragraph (3-5 sentences) capturing what was discussed, decided, or worked on. Write it so a future search like "what did we discuss about X" would find it. Factual, specific, no fluff. If the conversation has no substance worth recalling later, reply with exactly: SKIP`


// callFlashSummary returns a one-paragraph summary of the conversation.
func callFlashSummary(ctx context.Context, orKey, convo string) (string, error) {
	reqBody := map[string]interface{}{
		"model":  flashModel,
		"stream": false,
		"messages": []map[string]string{
			{"role": "system", "content": episodePrompt},
			{"role": "user", "content": "Conversation:\n" + convo},
		},
		"provider": map[string]interface{}{"order": []string{"DeepSeek"}, "require_parameters": true},
	}
	jb, _ := json.Marshal(reqBody)
	req, _ := http.NewRequestWithContext(ctx, "POST", "https://openrouter.ai/api/v1/chat/completions", bytes.NewReader(jb))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+orKey)
	req.Header.Set("HTTP-Referer", "https://grams.chat")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil || len(parsed.Choices) == 0 {
		return "", fmt.Errorf("bad summary response")
	}
	return strings.TrimSpace(parsed.Choices[0].Message.Content), nil
}
