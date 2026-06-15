// internal/memory/episodes.go
//
// Per-conversation episode worker (replaces time-based episodes).
//
// Every 2h, for each user with a DEK in Redis (active), pull their opencode
// sessions from their container. For each session whose message count changed
// since we last summarized it, re-summarize -> embed -> supersede the prior
// episode. New sessions get a new episode. Unchanged sessions are skipped (free).
//
// Scales by activity: idle users (no DEK) and unchanged sessions cost nothing.
package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

const episodeInterval = 2 * time.Hour

// ocSession is the subset of opencode's /session list we use.
type ocSession struct {
	ID   string `json:"id"`
	Slug string `json:"slug"`
}

// ocMessage is the subset of /session/:id/message we use.
type ocMessage struct {
	Info struct {
		ID      string `json:"id"`
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"info"`
	Parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"parts"`
}

// StartEpisodeWorker launches the 2h per-conversation episode worker.
func (s *Store) StartEpisodeWorker(orKey string) {
	go func() {
		t := time.NewTicker(episodeInterval)
		defer t.Stop()
		for range t.C {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			s.runEpisodePass(ctx, orKey)
			cancel()
		}
	}()
}

// runEpisodePass processes all active users' changed sessions.
func (s *Store) runEpisodePass(ctx context.Context, orKey string) {
	// active users = running container + DEK in Redis.
	rows, err := s.pool.Query(ctx, `
		SELECT c.user_id, h.internal_ip, c.port
		FROM containers c JOIN hosts h ON h.id = c.host_id
		WHERE c.status = 'running'`)
	if err != nil {
		return
	}
	type tgt struct {
		uid  int64
		ip   string
		port int
	}
	var targets []tgt
	for rows.Next() {
		var t tgt
		if rows.Scan(&t.uid, &t.ip, &t.port) == nil {
			targets = append(targets, t)
		}
	}
	rows.Close()

	for _, t := range targets {
		if !s.keys.Has(ctx, t.uid) {
			continue // no DEK -> skip (can't encrypt)
		}
		s.episodesForUser(ctx, t.uid, t.ip, t.port, orKey)
	}
}

// episodesForUser pulls one user's sessions and summarizes the changed ones.
func (s *Store) episodesForUser(ctx context.Context, uid int64, ip string, port int, orKey string) {
	base := fmt.Sprintf("http://%s:%d", ip, port)
	sessions, err := ocGetSessions(ctx, base)
	if err != nil {
		return
	}
	for _, sess := range sessions {
		msgs, err := ocGetMessages(ctx, base, sess.ID)
		if err != nil || len(msgs) == 0 {
			continue
		}
		// change detection: compare current msg count to last summarized count.
		var lastCount int
		var lastMemID *int64
		_ = s.pool.QueryRow(ctx,
			`SELECT msg_count, memory_id FROM session_episodes WHERE user_id=$1 AND session_id=$2`,
			uid, sess.ID).Scan(&lastCount, &lastMemID)
		if len(msgs) == lastCount {
			continue // unchanged -> skip (free)
		}
		convo := ocMessagesToText(msgs)
		if strings.TrimSpace(convo) == "" {
			continue
		}
		summary, err := callFlashSummary(ctx, orKey, convo)
		if err != nil || summary == "" || strings.EqualFold(strings.TrimSpace(summary), "SKIP") {
			// still record the count so we don't re-summarize an unchanged trivial chat
			s.upsertSessionEpisode(ctx, uid, sess.ID, lastMemID, len(msgs))
			continue
		}
		newID, err := s.storeEpisodeReturningID(ctx, uid, summary, orKey, sess.ID)
		if err != nil {
			continue
		}
		// supersede the previous episode for this session, if any.
		if lastMemID != nil {
			_, _ = s.pool.Exec(ctx, `UPDATE user_memory SET superseded_by=$2, updated_at=now() WHERE id=$1`, *lastMemID, newID)
		}
		s.upsertSessionEpisode(ctx, uid, sess.ID, &newID, len(msgs))
		log.Printf("episode: user %d session %s summarized (%d msgs)", uid, sess.ID, len(msgs))
	}
}

func (s *Store) upsertSessionEpisode(ctx context.Context, uid int64, sessID string, memID *int64, count int) {
	_, _ = s.pool.Exec(ctx, `
		INSERT INTO session_episodes (user_id, session_id, memory_id, msg_count, updated_at)
		VALUES ($1,$2,$3,$4,now())
		ON CONFLICT (user_id, session_id)
		DO UPDATE SET memory_id=EXCLUDED.memory_id, msg_count=EXCLUDED.msg_count, updated_at=now()`,
		uid, sessID, memID, count)
}

// storeEpisodeReturningID is StoreEpisode but returns the new row id (for supersede).
func (s *Store) storeEpisodeReturningID(ctx context.Context, userID int64, summary, orKey, sessID string) (int64, error) {
	dek, err := s.keys.Get(ctx, userID)
	if err != nil {
		return 0, err
	}
	blob, err := EncryptContent(dek, []byte(summary))
	if err != nil {
		return 0, err
	}
	vec, embErr := Embed(ctx, orKey, summary)
	var id int64
	if embErr != nil {
		err = s.pool.QueryRow(ctx, `
			INSERT INTO user_memory (user_id, mem_type, category, content_enc, content_nonce, confidence, source_session)
			VALUES ($1,'episode','other',$2,$3,1.0,$4) RETURNING id`,
			userID, blob, []byte{}, sessID).Scan(&id)
		return id, err
	}
	err = s.pool.QueryRow(ctx, `
		INSERT INTO user_memory (user_id, mem_type, category, content_enc, content_nonce, embedding, confidence, source_session)
		VALUES ($1,'episode','other',$2,$3,$4::vector,1.0,$5) RETURNING id`,
		userID, blob, []byte{}, vectorLiteral(vec), sessID).Scan(&id)
	return id, err
}

// --- opencode HTTP helpers ---

func ocGetSessions(ctx context.Context, base string) ([]ocSession, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", base+"/session", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	var out []ocSession
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func ocGetMessages(ctx context.Context, base, sessID string) ([]ocMessage, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", base+"/session/"+sessID+"/message", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	var out []ocMessage
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ocMessagesToText flattens opencode messages. Content may be in info.content or
// in parts[].text (opencode uses parts for structured messages).
func ocMessagesToText(msgs []ocMessage) string {
	var sb strings.Builder
	for _, m := range msgs {
		role := m.Info.Role
		if role == "" {
			continue
		}
		text := m.Info.Content
		if text == "" {
			for _, p := range m.Parts {
				if p.Type == "text" && p.Text != "" {
					text += p.Text + " "
				}
			}
		}
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}
		sb.WriteString(role)
		sb.WriteString(": ")
		sb.WriteString(text)
		sb.WriteString("\n")
	}
	return sb.String()
}
