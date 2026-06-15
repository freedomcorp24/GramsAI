// internal/memory/embed.go
//
// Phase 2: episode summaries + embeddings (semantic search over past chats).
//
// Embeddings via OpenRouter's OpenAI-compatible endpoint:
//   POST https://openrouter.ai/api/v1/embeddings  model=openai/text-embedding-3-small (1536-dim)
// The 1536 dim matches the schema's vector(1536). Embedding text is non-reversible
// (you can't recover the source from the vector), but the summary text does transit
// to OpenRouter at embed time — same exposure as the LLM calls already have.
package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

const embedModel = "openai/text-embedding-3-small" // 1536-dim, matches schema

// Embed returns the 1536-dim embedding for a text via OpenRouter. Empty/oversize
// text is truncated. Returns nil on error (caller skips the embedding).
func Embed(ctx context.Context, orKey, text string) ([]float32, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, fmt.Errorf("empty text")
	}
	// hard cap input (~8k token model; keep well under)
	if len(text) > 24000 {
		text = text[:24000]
	}
	reqBody := map[string]interface{}{
		"model": embedModel,
		"input": text,
	}
	jb, _ := json.Marshal(reqBody)
	req, _ := http.NewRequestWithContext(ctx, "POST", "https://openrouter.ai/api/v1/embeddings", bytes.NewReader(jb))
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
		Data []struct {
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil || len(parsed.Data) == 0 || len(parsed.Data[0].Embedding) == 0 {
		return nil, fmt.Errorf("bad embedding response")
	}
	return parsed.Data[0].Embedding, nil
}

// vectorLiteral renders a []float32 as a pgvector literal: [0.1,0.2,...]
func vectorLiteral(v []float32) string {
	var sb strings.Builder
	sb.WriteByte('[')
	for i, f := range v {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(strconv.FormatFloat(float64(f), 'f', -1, 32))
	}
	sb.WriteByte(']')
	return sb.String()
}

// StoreEpisode encrypts the summary, embeds it, and inserts an 'episode' row with
// the vector. Used by the extraction worker. DEK must be in Redis.
func (s *Store) StoreEpisode(ctx context.Context, userID int64, summary, orKey, sourceSession string) error {
	dek, err := s.keys.Get(ctx, userID)
	if err != nil {
		return err
	}
	blob, err := EncryptContent(dek, []byte(summary))
	if err != nil {
		return err
	}
	vec, err := Embed(ctx, orKey, summary)
	if err != nil {
		// store without embedding rather than fail; it just won't be vector-searchable
		_, e := s.pool.Exec(ctx, `
			INSERT INTO user_memory (user_id, mem_type, category, content_enc, content_nonce, confidence, source_session)
			VALUES ($1,'episode','other',$2,$3,1.0,$4)`,
			userID, blob, []byte{}, sourceSession)
		return e
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO user_memory (user_id, mem_type, category, content_enc, content_nonce, embedding, confidence, source_session)
		VALUES ($1,'episode','other',$2,$3,$4::vector,1.0,$5)`,
		userID, blob, []byte{}, vectorLiteral(vec), sourceSession)
	return err
}

// SearchEpisodes embeds the query and returns the top-K most similar episodes for
// a user (cosine distance via pgvector), decrypted. Used by /api/memory/search.
func (s *Store) SearchEpisodes(ctx context.Context, userID int64, query, orKey string, k int) ([]Memory, error) {
	dek, err := s.keys.Get(ctx, userID)
	if err != nil {
		return nil, err
	}
	qvec, err := Embed(ctx, orKey, query)
	if err != nil {
		return nil, err
	}
	if k <= 0 {
		k = 5
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, mem_type, category, content_enc, confidence, created_at
		FROM user_memory
		WHERE user_id=$1 AND mem_type='episode' AND superseded_by IS NULL AND embedding IS NOT NULL
		ORDER BY embedding <=> $2::vector
		LIMIT $3`, userID, vectorLiteral(qvec), k)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Memory
	for rows.Next() {
		var m Memory
		var blob []byte
		if err := rows.Scan(&m.ID, &m.Type, &m.Category, &blob, &m.Confidence, &m.CreatedAt); err != nil {
			continue
		}
		pt, derr := DecryptContent(dek, blob)
		if derr != nil {
			continue
		}
		m.Content = string(pt)
		out = append(out, m)
	}
	return out, nil
}
