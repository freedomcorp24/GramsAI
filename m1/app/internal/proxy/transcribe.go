package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// lookupUserByID is the cookie-path twin of lookupUserByToken: the browser app
// authenticates with a session cookie (resolved to a uid by getUID), so we load
// the same llmUser fields by id to run the identical access gate.
func lookupUserByID(ctx context.Context, pool *pgxpool.Pool, uid int64) (*llmUser, error) {
	u := &llmUser{}
	err := pool.QueryRow(ctx, `
		SELECT id, tier, status, unmetered, paid_until,
		       compute_budget_micros, compute_used_micros,
		       daily_used_micros, daily_limit_micros, daily_reset,
		       budget_reset, monthly_budget_micros,
		       storage_used_bytes, storage_extra_bytes
		FROM users WHERE id = $1`, uid).
		Scan(&u.ID, &u.Tier, &u.Status, &u.Unmetered, &u.PaidUntil,
			&u.BudgetMicros, &u.UsedMicros,
			&u.DailyUsedMicros, &u.DailyLimitMicros, &u.DailyReset,
			&u.BudgetReset, &u.MonthlyBudget,
			&u.StorageUsed, &u.StorageExtra)
	if err != nil {
		return nil, err
	}
	return u, nil
}

// TranscribeHandler is the browser-facing speech-to-text route. It is cookie
// authenticated (getUID), enforces the SAME access gate as the chat path, then
// forwards base64 audio to OpenRouter's dedicated /audio/transcriptions endpoint
// using the shared key, returns {text}, and meters usage.cost via Meter so voice
// is billed identically to chat. Model is the "Voice" alias (hot-swap in the
// AliasToModel map, same as every other model).
func TranscribeHandler(pool *pgxpool.Pool, openrouterKey string, getUID func(*http.Request) int64) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uid := getUID(r)
		if uid <= 0 {
			http.Error(w, `{"error":{"message":"unauthorized"}}`, http.StatusUnauthorized)
			return
		}
		ctx := r.Context()
		u, err := lookupUserByID(ctx, pool, uid)
		if err != nil {
			http.Error(w, `{"error":{"message":"user not found"}}`, http.StatusUnauthorized)
			return
		}

		// --- ACCESS ENFORCEMENT: identical to chat (suspend -> unmetered -> gates) ---
		if u.Status == "suspended" {
			http.Error(w, `{"error":{"message":"account suspended"}}`, http.StatusForbidden)
			return
		}
		if !u.Unmetered {
			if u.PaidUntil == nil || !u.PaidUntil.After(time.Now()) {
				http.Error(w, `{"error":{"message":"subscription required or expired","type":"payment_required"}}`, http.StatusPaymentRequired)
				return
			}
			today := time.Now().UTC().Truncate(24 * time.Hour)
			if u.DailyReset.Before(today) {
				u.DailyUsedMicros = 0
				_, _ = pool.Exec(ctx, `UPDATE users SET daily_used_micros=0, daily_used_cents=0, daily_reset=$2 WHERE id=$1`, u.ID, today)
			}
			if u.BudgetReset != nil && time.Now().After(*u.BudgetReset) && u.MonthlyBudget > 0 {
				u.BudgetMicros = u.MonthlyBudget
				u.UsedMicros = 0
				_, _ = pool.Exec(ctx, `UPDATE users SET compute_budget_micros=$2, compute_used_micros=0, compute_budget_cents=$2/10000, compute_used_cents=0, budget_reset=now()+interval '30 days' WHERE id=$1`, u.ID, u.MonthlyBudget)
			}
			if u.UsedMicros >= u.BudgetMicros {
				http.Error(w, `{"error":{"message":"compute budget exhausted","type":"insufficient_quota"}}`, http.StatusPaymentRequired)
				return
			}
			if u.DailyLimitMicros > 0 && u.DailyUsedMicros >= u.DailyLimitMicros {
				http.Error(w, `{"error":{"message":"daily limit reached","type":"insufficient_quota"}}`, http.StatusPaymentRequired)
				return
			}
		}

		// --- READ CLIENT BODY: {audio: <base64 raw bytes>, format: "webm"|"mp4"|..., language?: "en"} ---
		var in struct {
			Audio    string `json:"audio"`
			Format   string `json:"format"`
			Language string `json:"language"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 30<<20)).Decode(&in); err != nil || in.Audio == "" {
			http.Error(w, `{"error":{"message":"audio (base64) required"}}`, http.StatusBadRequest)
			return
		}
		if in.Format == "" {
			in.Format = "webm"
		}

		// --- BUILD OPENROUTER TRANSCRIPTION REQUEST ---
		realModel := AliasToModel["Voice"]
		if realModel == "" {
			realModel = "openai/whisper-large-v3"
		}
		reqBody := map[string]any{
			"model":       realModel,
			"input_audio": map[string]string{"data": in.Audio, "format": in.Format},
		}
		if in.Language != "" {
			reqBody["language"] = in.Language // else omit -> whisper auto-detects
		}
		jsonBody, _ := json.Marshal(reqBody)

		// Upstream timeout is 60s; give a little headroom for transfer.
		upCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
		defer cancel()
		upReq, _ := http.NewRequestWithContext(upCtx, "POST",
			"https://openrouter.ai/api/v1/audio/transcriptions", bytes.NewReader(jsonBody))
		upReq.Header.Set("Authorization", "Bearer "+openrouterKey)
		upReq.Header.Set("Content-Type", "application/json")

		resp, err := http.DefaultClient.Do(upReq)
		if err != nil {
			http.Error(w, `{"error":{"message":"transcription upstream failed"}}`, http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		respBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		if resp.StatusCode != http.StatusOK {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(resp.StatusCode)
			w.Write(respBytes)
			return
		}

		// --- PARSE {text, usage:{cost,...}} + METER (same path as chat) ---
		var out struct {
			Text  string `json:"text"`
			Usage *struct {
				Cost         float64 `json:"cost"`
				InputTokens  int     `json:"input_tokens"`
				OutputTokens int     `json:"output_tokens"`
			} `json:"usage"`
		}
		_ = json.Unmarshal(respBytes, &out)
		if out.Usage != nil {
			costMicros := int64(out.Usage.Cost*1_000_000 + 0.5)
			if costMicros > 0 || out.Usage.InputTokens > 0 || out.Usage.OutputTokens > 0 {
				go Meter(pool, u.ID, "Voice", realModel, out.Usage.InputTokens, out.Usage.OutputTokens, costMicros)
			}
		}

		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"text":` + jsonString(out.Text) + `}`))
	}
}

// jsonString safely JSON-encodes a string (with quotes).
func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
