// internal/proxy/llm.go
// OpenAI-compatible LLM gateway endpoint.
// Containers' OpenCode points here (baseURL) with a per-user token (apiKey).
// Flow: auth token -> enforce access (suspend/paid/unmetered) -> check budget/daily/storage
// -> map alias model -> forward to OpenRouter with the SHARED key -> stream back
// -> meter actual cost (usage.cost).
package proxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"gramsai/internal/memory"
)

// AliasToModel maps the 14 USER-FACING specialty names to real OpenRouter model IDs.
// Containers and users NEVER see the right-hand side.
var AliasToModel = map[string]string{
	"General":    "deepseek/deepseek-v4-flash-20260423",
	"Code":       "deepseek/deepseek-v4-pro-20260423",
	"Roleplay":   "deepseek/deepseek-v4-flash-20260423",
	"Adult":      "deepseek/deepseek-v4-flash-20260423",
	"Uncensored": "deepseek/deepseek-v4-flash-20260423",
	"Research":   "deepseek/deepseek-v4-pro-20260423",
	"Medical":    "deepseek/deepseek-v4-pro-20260423",
	"Legal":      "qwen/qwen3-235b-a22b",
	"Financial":  "deepseek/deepseek-v4-pro-20260423",
	"Data":       "qwen/qwen3-235b-a22b",
	"Writer":     "qwen/qwen3-235b-a22b",
	"Translate":  "qwen/qwen3-235b-a22b",
	"Vision":     "meta-llama/llama-4-maverick",
	"Image Gen":  "black-forest-labs/flux.2-pro",
}

// tierAllows maps tier -> set of specialties the tier may use. Basic is limited
// to General; all paid-up tiers above basic get the full set. Enforced here so
// the frontend grey-out can't be bypassed.
var tierAllows = map[string]map[string]bool{
	"basic": {"General": true},
}

const giB = int64(1) << 30

// freeStorageBytes is the 1GB baseline INCLUDED on every tier. Storage is fully
// decoupled from tier: effective quota = freeStorageBytes + storage_extra_bytes
// (the purchased add-on). A Basic user with a +100GB add-on gets 101GB; a Pro
// user with no add-on gets 1GB.
const freeStorageBytes = 1 * giB

// storageQuota returns the effective byte quota for a user (free baseline + purchased extra).
func storageQuota(extra int64) int64 {
	return freeStorageBytes + extra
}

// specialtyAllowed returns true if the tier may use the alias. Any tier not in
// tierAllows (pro/max/ultra/unknown) gets everything.
func specialtyAllowed(tier, alias string) bool {
	set, limited := tierAllows[tier]
	if !limited {
		return true
	}
	return set[alias]
}

type llmUser struct {
	ID               int64
	Tier             string
	Status           string
	Unmetered        bool
	PaidUntil        *time.Time
	BudgetMicros     int64
	UsedMicros       int64
	DailyUsedMicros  int64
	DailyLimitMicros int64
	DailyReset       time.Time
	BudgetReset      *time.Time
	MonthlyBudget    int64
	StorageUsed      int64
	StorageExtra     int64
}

// LLMHandler returns the /v1/chat/completions handler.
func LLMHandler(pool *pgxpool.Pool, store *memory.Store, openrouterKey string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// --- 1. AUTH: per-user token from Authorization: Bearer <token> ---
		auth := r.Header.Get("Authorization")
		token := strings.TrimPrefix(auth, "Bearer ")
		token = strings.TrimSpace(token)
		if token == "" {
			http.Error(w, `{"error":{"message":"missing api key"}}`, http.StatusUnauthorized)
			return
		}

		ctx := r.Context()
		u, err := lookupUserByToken(ctx, pool, token)
		if err != nil {
			http.Error(w, `{"error":{"message":"invalid api key"}}`, http.StatusUnauthorized)
			return
		}

		// --- 2. ACCESS ENFORCEMENT: suspend -> unmetered bypass -> payment gate ---
		if u.Status == "suspended" {
			http.Error(w, `{"error":{"message":"account suspended","type":"account_suspended"}}`, http.StatusForbidden)
			return
		}

		// Unmetered (admin-comped, unlimited) skips the payment gate AND budget/daily/storage.
		if !u.Unmetered {
			// Payment gate: must have a valid, unexpired subscription.
			if u.PaidUntil == nil || !u.PaidUntil.After(time.Now()) {
				http.Error(w, `{"error":{"message":"subscription required or expired","type":"payment_required"}}`, http.StatusPaymentRequired)
				return
			}

			// --- 2b. LIMIT CHECK: total budget + daily cap ---
			today := time.Now().UTC().Truncate(24 * time.Hour)
			if u.DailyReset.Before(today) {
				u.DailyUsedMicros = 0
				_, _ = pool.Exec(ctx, `UPDATE users SET daily_used_micros=0, daily_used_cents=0, daily_reset=$2 WHERE id=$1`, u.ID, today)
			}
			// Monthly compute-budget refill: when the 30-day cycle elapses, reset
			// the budget to the tier's monthly allowance (use-it-or-lose-it; topups
			// from the prior cycle do not carry over).
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

			// --- 2c. STORAGE GATE: cached usage vs free baseline + purchased extra ---
			// storage_used_bytes is refreshed by the gateway's background usage poller.
			// The >0 guard means we never block before the poller has measured at least
			// once (no false lockouts on a freshly provisioned account).
			if u.StorageUsed > 0 && u.StorageUsed >= storageQuota(u.StorageExtra) {
				http.Error(w, `{"error":{"message":"storage limit reached — upgrade storage to continue","type":"storage_full"}}`, http.StatusPaymentRequired)
				return
			}
		}

		// --- 3. READ BODY + MAP ALIAS MODEL ---
		var body map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, `{"error":{"message":"bad request"}}`, http.StatusBadRequest)
			return
		}
		alias, _ := body["model"].(string)
		// alias may arrive as "gramsai/Code" or "Code" — take last path segment
		if i := strings.LastIndex(alias, "/"); i >= 0 {
			alias = alias[i+1:]
		}
		// Tier specialty lock (skip for unmetered/admin accounts).
		if !u.Unmetered && !specialtyAllowed(u.Tier, alias) {
			http.Error(w, `{"error":{"message":"this specialty requires a higher tier","type":"tier_locked"}}`, http.StatusForbidden)
			return
		}
		realModel := AliasToModel[alias]
		if realModel == "" {
			realModel = AliasToModel["General"]
		}
		body["model"] = realModel
		if alias == "Image Gen" {
			body["modalities"] = []string{"image"}
		}
		// force usage accounting on (authoritative cost in final SSE chunk)
		body["usage"] = map[string]interface{}{"include": true}

		// GRAMSAI_MEM_INJECT: prepend the user's memory facts as a system message.
		// SAFE: bounded by a 150ms deadline and fail-open — any timeout/error/no-key
		// skips injection and lets the chat proceed. Memory never blocks a request.
		if store != nil {
			memCtx, memCancel := context.WithTimeout(ctx, 150*time.Millisecond)
			facts, ferr := store.LoadMemories(memCtx, u.ID, "fact")
			memCancel()
			if ferr == nil && len(facts) > 0 {
				var sb strings.Builder
				sb.WriteString("[What you know about this user from past conversations. Use naturally; do not recite verbatim or mention stored memory.]\n")
				for _, f := range facts {
					sb.WriteString("- (")
					sb.WriteString(f.Category)
					sb.WriteString(") ")
					sb.WriteString(f.Content)
					sb.WriteString("\n")
				}
				if msgs, ok := body["messages"].([]interface{}); ok {
					memMsg := map[string]interface{}{"role": "system", "content": sb.String()}
					body["messages"] = append([]interface{}{memMsg}, msgs...)
				}
			}
		}
		// GRAMSAI_PROC_INJECT: for technical specialties, also inject procedure memory
		// (tech stack, build/deploy commands, conventions). Skipped for non-technical
		// specialties where it's noise. Bounded + fail-open like fact injection.
		switch alias {
		case "Code", "Security", "Data", "Research", "Financial", "Medical":
			if store != nil {
				pCtx, pCancel := context.WithTimeout(ctx, 150*time.Millisecond)
				procs, perr := store.LoadMemories(pCtx, u.ID, "procedure")
				pCancel()
				if perr == nil && len(procs) > 0 {
					var pb strings.Builder
					pb.WriteString("[Technical context about this user's environment and workflow. Apply when relevant; don't recite.]\n")
					for _, p := range procs {
						pb.WriteString("- ")
						pb.WriteString(p.Content)
						pb.WriteString("\n")
					}
					if msgs, ok := body["messages"].([]interface{}); ok {
						pMsg := map[string]interface{}{"role": "system", "content": pb.String()}
						body["messages"] = append([]interface{}{pMsg}, msgs...)
					}
				}
			}
		}

		// GRAMSAI_MEM_SNAPSHOT: capture the conversation for the extraction worker.
		// Fail-open, bounded; never affects the request.
		if store != nil {
			if msgs, ok := body["messages"].([]interface{}); ok {
				snapCtx, snapCancel := context.WithTimeout(ctx, 100*time.Millisecond)
				store.SnapshotConversation(snapCtx, u.ID, msgs)
				snapCancel()
			}
		}
		// GRAMSAI_PROVIDER_ORDER: pin OpenRouter to the reliable provider to avoid
		// "Upstream idle timeout exceeded" during long reasoning. order prefers the
		// official DeepSeek endpoint (best uptime); require_parameters ensures the
		// provider honors our reasoning+tool params. Only set if caller did not.
		if _, ok := body["provider"]; !ok && alias != "Image Gen" {
			body["provider"] = map[string]interface{}{
				"order":              []string{"DeepSeek"},
				"require_parameters": true,
			}
		}

		streaming, _ := body["stream"].(bool)
		// GRAMSAI_IMAGE_REWRITE: image gen must be non-streaming so we can convert
		// the OpenRouter images[] response into a markdown data-url completion that
		// opencode actually renders (it discards model file/image output parts).
		if alias == "Image Gen" {
			body["stream"] = false
			streaming = false
		}
		jsonBody, _ := json.Marshal(body)

		// --- 4. FORWARD TO OPENROUTER WITH SHARED KEY ---
		// GRAMSAI_UPSTREAM_RETRY: "Upstream idle timeout exceeded" arrives as a 5xx
		// when the provider stalls. Retry the request (rebuilt from jsonBody each
		// attempt, since the body reader is consumed) on transport error or HTTP>=500.
		var resp *http.Response
		const maxAttempts = 3
		for attempt := 1; attempt <= maxAttempts; attempt++ {
			upReq, _ := http.NewRequestWithContext(ctx, "POST",
				"https://openrouter.ai/api/v1/chat/completions", bytes.NewReader(jsonBody))
			upReq.Header.Set("Content-Type", "application/json")
			upReq.Header.Set("Authorization", "Bearer "+openrouterKey)
			upReq.Header.Set("HTTP-Referer", "https://grams.chat")
			upReq.Header.Set("X-Title", "grams")

			resp, err = http.DefaultClient.Do(upReq)
			if err == nil && resp.StatusCode < 500 {
				break // success (or a non-retryable 4xx) -> proceed
			}
			if resp != nil {
				resp.Body.Close()
			}
			if attempt < maxAttempts {
				time.Sleep(time.Duration(attempt) * 500 * time.Millisecond)
			}
		}
		if err != nil || resp == nil {
			http.Error(w, `{"error":{"message":"upstream error"}}`, http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()

		// --- 5. STREAM BACK + capture usage.cost from final chunk ---
		var costMicros int64
		var inTok, outTok int
		if streaming {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("Connection", "keep-alive")
			w.Header().Set("X-Accel-Buffering", "no")
			w.WriteHeader(resp.StatusCode)
			flusher, _ := w.(http.Flusher)

			reader := bufio.NewReader(resp.Body)
			for {
				line, err := reader.ReadBytes('\n')
				if len(line) > 0 {
					w.Write(line)
					if flusher != nil {
						flusher.Flush()
					}
					if c, it, ot, ok := parseUsage(line); ok {
						costMicros, inTok, outTok = c, it, ot
					}
				}
				if err != nil {
					break
				}
			}
		} else {
			data, _ := io.ReadAll(resp.Body)
			if alias == "Image Gen" {
				data = rewriteImageResponse(data)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(resp.StatusCode)
			w.Write(data)
			if c, it, ot, ok := parseUsageJSON(data); ok {
				costMicros, inTok, outTok = c, it, ot
			}
		}

		// --- 6. METER: write usage_logs + increment spend (best-effort, async) ---
		if costMicros > 0 || inTok > 0 || outTok > 0 {
			go meter(pool, u.ID, alias, realModel, inTok, outTok, costMicros)
		}
	}
}

func lookupUserByToken(ctx context.Context, pool *pgxpool.Pool, token string) (*llmUser, error) {
	u := &llmUser{}
	err := pool.QueryRow(ctx, `
		SELECT id, tier, status, unmetered, paid_until,
		       compute_budget_micros, compute_used_micros,
		       daily_used_micros, daily_limit_micros, daily_reset,
		       budget_reset, monthly_budget_micros,
		       storage_used_bytes, storage_extra_bytes
		FROM users
		WHERE api_token = $1
		   OR id = (SELECT user_id FROM api_tokens WHERE token = $1 AND revoked = false)`, token).
		Scan(&u.ID, &u.Tier, &u.Status, &u.Unmetered, &u.PaidUntil,
			&u.BudgetMicros, &u.UsedMicros,
			&u.DailyUsedMicros, &u.DailyLimitMicros, &u.DailyReset,
			&u.BudgetReset, &u.MonthlyBudget,
			&u.StorageUsed, &u.StorageExtra)
	if err != nil {
		return nil, err
	}
	// best-effort: mark a user-created token as recently used (no-op if this
	// was the container's users.api_token).
	_, _ = pool.Exec(ctx, `UPDATE api_tokens SET last_used = now() WHERE token = $1 AND revoked = false`, token)
	return u, nil
}

func meter(pool *pgxpool.Pool, userID int64, specialty, model string, inTok, outTok int, costMicros int64) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	costCents := (costMicros + 9999) / 10000
	_, err := pool.Exec(ctx, `
		INSERT INTO usage_logs (user_id, specialty, model, input_tokens, output_tokens, cost_cents, cost_micros)
		VALUES ($1,$2,$3,$4,$5,$6,$7)`, userID, specialty, model, inTok, outTok, costCents, costMicros)
	if err != nil {
		log.Printf("meter: usage_logs insert failed: %v", err)
	}
	_, err = pool.Exec(ctx, `
		UPDATE users SET compute_used_micros = compute_used_micros + $2,
		                 daily_used_micros   = daily_used_micros + $2,
		                 compute_used_cents  = (compute_used_micros + $2) / 10000,
		                 daily_used_cents    = (daily_used_micros + $2) / 10000,
		                 updated_at = now()
		WHERE id = $1`, userID, costMicros)
	if err != nil {
		log.Printf("meter: budget update failed: %v", err)
	}
}

func parseUsage(line []byte) (costMicros int64, inTok, outTok int, ok bool) {
	s := bytes.TrimSpace(line)
	if !bytes.HasPrefix(s, []byte("data:")) {
		return 0, 0, 0, false
	}
	payload := bytes.TrimSpace(s[5:])
	if bytes.Equal(payload, []byte("[DONE]")) {
		return 0, 0, 0, false
	}
	return parseUsageJSON(payload)
}

func parseUsageJSON(data []byte) (costMicros int64, inTok, outTok int, ok bool) {
	var obj struct {
		Usage *struct {
			PromptTokens     int     `json:"prompt_tokens"`
			CompletionTokens int     `json:"completion_tokens"`
			Cost             float64 `json:"cost"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(data, &obj); err != nil || obj.Usage == nil {
		return 0, 0, 0, false
	}
	micros := int64(obj.Usage.Cost*1_000_000 + 0.5)
	return micros, obj.Usage.PromptTokens, obj.Usage.CompletionTokens, true
}


// ResolveUID resolves a Bearer token to a user id (exported for other packages,
// e.g. the memory search endpoint). Returns 0 on failure.
func ResolveUID(ctx context.Context, pool *pgxpool.Pool, token string) (int64, error) {
	u, err := lookupUserByToken(ctx, pool, token)
	if err != nil {
		return 0, err
	}
	return u.ID, nil
}


// GRAMSAI_IMAGE_REWRITE: OpenRouter returns generated images in
// choices[].message.images[].image_url.url (a data: URL). opencode discards
// model image output, so we move the image into message.content as a markdown
// image. The result renders wherever opencode renders markdown (tool output).
func rewriteImageResponse(data []byte) []byte {
	var obj map[string]interface{}
	if err := json.Unmarshal(data, &obj); err != nil {
		return data
	}
	choices, _ := obj["choices"].([]interface{})
	if len(choices) == 0 {
		return data
	}
	ch0, _ := choices[0].(map[string]interface{})
	if ch0 == nil {
		return data
	}
	msg, _ := ch0["message"].(map[string]interface{})
	if msg == nil {
		return data
	}
	imgs, _ := msg["images"].([]interface{})
	if len(imgs) == 0 {
		return data
	}
	var md strings.Builder
	txt, _ := msg["content"].(string)
	if txt != "" {
		md.WriteString(txt)
		md.WriteString("\n\n")
	}
	for _, it := range imgs {
		im, _ := it.(map[string]interface{})
		if im == nil {
			continue
		}
		iu, _ := im["image_url"].(map[string]interface{})
		if iu == nil {
			continue
		}
		url, _ := iu["url"].(string)
		if url != "" {
			md.WriteString("![generated image](")
			md.WriteString(url)
			md.WriteString(")\n")
		}
	}
	msg["content"] = md.String()
	delete(msg, "images")
	out, err := json.Marshal(obj)
	if err != nil {
		return data
	}
	return out
}
