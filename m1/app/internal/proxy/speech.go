package proxy

import (
	"log"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// per-model default voice + output format. Gemini = pcm only; Grok = mp3.
type ttsModelSpec struct {
	model        string
	format       string // response_format sent to OpenRouter
	contentType  string // what we return to the browser
	defaultVoice string
	voices       map[string]bool // allowed voices (lowercased) for validation; empty = allow any
}

var ttsPrimary = ttsModelSpec{
	model:        AliasToModel["Voice TTS"], // google/gemini-3.1-flash-tts-preview
	format:       "pcm",
	contentType:  "audio/pcm;rate=24000;channels=1",
	defaultVoice: "Kore",
	voices: map[string]bool{
		"zephyr": true, "puck": true, "charon": true, "kore": true, "fenrir": true,
		"leda": true, "orus": true, "aoede": true, "callirrhoe": true, "autonoe": true,
		"enceladus": true, "iapetus": true, "umbriel": true, "algieba": true, "despina": true,
		"erinome": true, "algenib": true, "rasalgethi": true, "laomedeia": true, "achernar": true,
		"alnilam": true, "schedar": true, "gacrux": true, "pulcherrima": true, "achird": true,
		"zubenelgenubi": true, "vindemiatrix": true, "sadachbia": true, "sadaltager": true, "sulafat": true,
	},
}

var ttsFallback = ttsModelSpec{
	model:        AliasToModel["Voice TTS Alt"], // x-ai/grok-voice-tts-1.0
	format:       "mp3",
	contentType:  "audio/mpeg",
	defaultVoice: "eve",
	voices:       map[string]bool{"eve": true, "ara": true, "rex": true, "sal": true, "leo": true},
}

// SpeechHandler is the browser-facing text-to-speech route. Cookie-authed via
// getUID, same access gate as chat. Tries the primary (Gemini, best multilingual)
// then auto-falls-back to Grok (permissive) on any refusal/error, so uncensored
// text always gets spoken. Returns raw audio bytes with the right Content-Type.
func SpeechHandler(pool *pgxpool.Pool, openrouterKey string, getUID func(*http.Request) int64) http.HandlerFunc {
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
		if u.Status == "suspended" {
			http.Error(w, `{"error":{"message":"account suspended"}}`, http.StatusForbidden)
			return
		}
		if !u.Unmetered {
			if u.PaidUntil == nil || !u.PaidUntil.After(time.Now()) {
				http.Error(w, `{"error":{"message":"subscription required or expired","type":"payment_required"}}`, http.StatusPaymentRequired)
				return
			}
			if u.UsedMicros >= u.BudgetMicros && u.BudgetMicros > 0 {
				http.Error(w, `{"error":{"message":"compute budget exhausted","type":"insufficient_quota"}}`, http.StatusPaymentRequired)
				return
			}
		}

		var in struct {
			Text  string `json:"text"`
			Voice string `json:"voice"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil || strings.TrimSpace(in.Text) == "" {
			http.Error(w, `{"error":{"message":"text required"}}`, http.StatusBadRequest)
			return
		}
		// cap input length (provider limits + cost guard)
		log.Printf("[TTS] uid=%d textlen=%d voice=%q", uid, len(in.Text), in.Voice)
		if len(in.Text) > 6000 {
			in.Text = in.Text[:6000]
		}

		// pick a voice valid for the active model; the same picker value may not be
		// valid across models, so each attempt validates against its own voice set.
		tryModel := func(spec ttsModelSpec) (*http.Response, error) {
			voice := strings.TrimSpace(in.Voice)
			if voice == "" || (len(spec.voices) > 0 && !spec.voices[strings.ToLower(voice)]) {
				voice = spec.defaultVoice
			}
			body, _ := json.Marshal(map[string]any{
				"model":           spec.model,
				"input":           in.Text,
				"voice":           voice,
				"response_format": spec.format,
			})
			upCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
			req, _ := http.NewRequestWithContext(upCtx, "POST",
				"https://openrouter.ai/api/v1/audio/speech", bytes.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+openrouterKey)
			req.Header.Set("Content-Type", "application/json")
			resp, err := http.DefaultClient.Do(req)
			if resp != nil { log.Printf("[TTS] model=%s voice=%s -> %d", spec.model, voice, resp.StatusCode) } else { log.Printf("[TTS] model=%s err=%v", spec.model, err) }
			if err != nil {
				cancel()
				return nil, err
			}
			// caller closes body + cancels via the returned response context chain
			_ = cancel
			return resp, nil
		}

		// --- attempt PRIMARY (Gemini) ---
		spec := ttsPrimary
		resp, err := tryModel(spec)
		if err != nil || resp == nil || resp.StatusCode != http.StatusOK {
			if resp != nil {
				io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
				resp.Body.Close()
			}
			// --- FALLBACK (Grok): primary refused/errored ---
			spec = ttsFallback
			resp, err = tryModel(spec)
			if err != nil || resp == nil || resp.StatusCode != http.StatusOK {
				if resp != nil {
					b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
					resp.Body.Close()
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(resp.StatusCode)
					w.Write(b)
					return
				}
				http.Error(w, `{"error":{"message":"tts upstream failed"}}`, http.StatusBadGateway)
				return
			}
		}
		defer resp.Body.Close()

		// stream audio bytes back to the browser
		w.Header().Set("Content-Type", spec.contentType)
		w.Header().Set("Cache-Control", "no-store")
		n, _ := io.Copy(w, resp.Body)

		// best-effort meter: TTS bills per input char; estimate cost from text length.
		// (response is raw audio, no usage JSON.) ~ rough micros; keeps budget honest.
		go meterTTS(pool, u.ID, spec.model, len(in.Text), n)
	}
}

// meterTTS records a best-effort TTS cost. OpenRouter TTS is per-character; we
// approximate at a conservative rate so the user's budget reflects voice usage.
func meterTTS(pool *pgxpool.Pool, userID int64, model string, inputChars int, audioBytes int64) {
	// conservative: ~$0.00002 per char upper-bound across providers -> micros
	costMicros := int64(inputChars) * 20
	if costMicros <= 0 {
		return
	}
	Meter(pool, userID, "Voice TTS", model, inputChars, 0, costMicros)
}
