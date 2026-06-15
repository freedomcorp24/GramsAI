// internal/pay/pay.go
// Provider-agnostic payment layer.
//
// BUNDLED MODEL: a subscription payment carries BOTH the tier and an optional
// storage add-on, charged in one transaction, sharing one expiry (paid_until).
// Storage is RECURRING: each subscription payment SETS storage_extra_bytes to
// exactly what was purchased (0 if none), so storage is re-paid every renewal
// and never persists past a lapse. Top-ups remain a separate one-time compute
// purchase.
//
// MID-CYCLE CHANGES (/account/change, settings section):
//   - net UPGRADE  -> prorated charge for remaining days ($1 floor); on confirm
//                     applies new tier+storage IMMEDIATELY, does NOT extend
//                     paid_until, raises budget ceiling but preserves used.
//   - net DOWNGRADE/lateral -> writes pending_tier/pending_storage_bytes, free,
//                     effective at next renewal; the next subscription payment
//                     supersedes & clears it.
package pay

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ---- pricing (USD cents) ----

// Monthly base prices per tier (locked). Yearly = 10x monthly (2 months free).
var monthlyCents = map[string]int64{
	"basic": 1900,
	"pro":   4900,
	"max":   9900,
	"ultra": 19900,
}

// Compute top-up packs: one-time dollars added to the current cycle's budget.
var topupPacks = map[string]int64{ // pack id -> USD cents
	"t10":  1000,
	"t25":  2500,
	"t50":  5000,
	"t100": 10000,
}

const giB = int64(1) << 30

// Storage add-ons. Monthly cents; yearly = 10x. "" or "s0" = free 1GB baseline (no extra).
var storagePacks = map[string]struct {
	Cents int64
	Bytes int64
}{
	"s5":   {500, 5 * giB},
	"s25":  {1500, 25 * giB},
	"s100": {3000, 100 * giB},
}

func tierCents(tier, period string) (int64, bool) {
	m, ok := monthlyCents[tier]
	if !ok {
		return 0, false
	}
	if period == "yearly" {
		return m * 10, true
	}
	return m, true
}

func storageCents(pack, period string) (int64, bool) {
	if pack == "" || pack == "s0" {
		return 0, true
	}
	p, ok := storagePacks[pack]
	if !ok {
		return 0, false
	}
	if period == "yearly" {
		return p.Cents * 10, true
	}
	return p.Cents, true
}

func storageBytesFor(pack string) int64 {
	if pack == "" || pack == "s0" {
		return 0
	}
	return storagePacks[pack].Bytes
}

// storagePackForBytes reverse-maps a stored byte grant back to its pack id so we
// can price a user's CURRENT add-on. Unknown/legacy values price as free (0).
func storagePackForBytes(b int64) string {
	switch b {
	case 5 * giB:
		return "s5"
	case 25 * giB:
		return "s25"
	case 100 * giB:
		return "s100"
	}
	return "" // 0 or unrecognized
}

// ---- service ----

type Service struct {
	pool        *pgxpool.Pool
	npAPIKey    string
	npIPNSecret string
	baseURL     string
}

func New(pool *pgxpool.Pool, npAPIKey, npIPNSecret, baseURL string) *Service {
	return &Service{pool: pool, npAPIKey: npAPIKey, npIPNSecret: npIPNSecret, baseURL: baseURL}
}

// ---- create payment (authed user) ----

type createReq struct {
	Kind    string `json:"kind"`    // subscription | topup
	Tier    string `json:"tier"`    // basic|pro|max|ultra      (subscription)
	Period  string `json:"period"`  // monthly|yearly           (subscription)
	Storage string `json:"storage"` // ""|s0|s5|s25|s100        (subscription add-on)
	Pack    string `json:"pack"`    // t10|t25|t50|t100         (topup)
	Coin    string `json:"coin"`    // xmr|btc|usdttrc20|...
}

func (s *Service) HandleCreate(getUID func(*http.Request) int64) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uid := getUID(r)
		if uid == 0 {
			http.Error(w, `{"error":"unauthorized"}`, 401)
			return
		}
		var b createReq
		if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
			http.Error(w, `{"error":"bad request"}`, 400)
			return
		}
		if b.Coin == "" {
			http.Error(w, `{"error":"coin required"}`, 400)
			return
		}

		var (
			cents        int64
			days         int
			budgetMicros int64
			storageBytes int64
			tier, period string
		)
		switch b.Kind {
		case "subscription":
			tc, ok := tierCents(b.Tier, b.Period)
			if !ok {
				http.Error(w, `{"error":"invalid tier"}`, 400)
				return
			}
			sc, ok := storageCents(b.Storage, b.Period)
			if !ok {
				http.Error(w, `{"error":"invalid storage option"}`, 400)
				return
			}
			cents = tc + sc
			tier = b.Tier
			period = b.Period
			storageBytes = storageBytesFor(b.Storage)
			if b.Period == "yearly" {
				days = 365
			} else {
				days = 30
			}
		case "topup":
			c, ok := topupPacks[b.Pack]
			if !ok {
				http.Error(w, `{"error":"invalid pack"}`, 400)
				return
			}
			cents = c
			budgetMicros = (c / 2) * 10000 // 50% margin: pay $X -> $X/2 compute (internal)
		default:
			http.Error(w, `{"error":"invalid kind"}`, 400)
			return
		}
		if cents <= 0 {
			http.Error(w, `{"error":"invalid plan"}`, 400)
			return
		}

		ctx := r.Context()
		payID, pd, err := s.createPaymentRow(ctx, uid, b.Kind, tier, period, days, budgetMicros, storageBytes, cents, b.Coin)
		if err != nil {
			log.Printf("pay: create: %v", err)
			http.Error(w, `{"error":"payment provider error"}`, 502)
			return
		}
		writeJSON(w, map[string]any{
			"ok":           true,
			"payment_id":   payID,
			"pay_address":  pd.PayAddress,
			"pay_amount":   pd.PayAmount,
			"pay_currency": pd.PayCurrency,
			"price_usd":    float64(cents) / 100.0,
		})
	}
}

// createPaymentRow inserts a waiting payment + opens the NOWPayments invoice.
// Shared by HandleCreate (subscription/topup) and HandleAccountChange (upgrade).
func (s *Service) createPaymentRow(ctx context.Context, uid int64, kind, tier, period string, days int, budgetMicros, storageBytes, cents int64, coin string) (int64, *paymentData, error) {
	var payID int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO payments (user_id, provider, kind, tier, period, days, budget_micros, storage_bytes, price_usd_cents, pay_currency, status)
		VALUES ($1,'nowpayments',$2,$3,$4,$5,$6,$7,$8,$9,'waiting') RETURNING id`,
		uid, kind, nullStr(tier), nullStr(period), days, budgetMicros, storageBytes, cents, nullStr(coin)).Scan(&payID)
	if err != nil {
		return 0, nil, err
	}
	orderID := fmt.Sprintf("gp_%d", payID)
	pd, err := s.npCreatePayment(ctx, orderID, cents, coin)
	if err != nil {
		_, _ = s.pool.Exec(ctx, `UPDATE payments SET status='failed', updated_at=now() WHERE id=$1`, payID)
		return 0, nil, err
	}
	_, _ = s.pool.Exec(ctx, `UPDATE payments SET provider_pid=$2, updated_at=now() WHERE id=$1`, payID, pd.PaymentID)
	return payID, pd, nil
}

// ---- account change (upgrade / downgrade) ----

type changeReq struct {
	Tier    string `json:"tier"`    // target tier
	Storage string `json:"storage"` // target storage add-on ""|s5|s25|s100
	Coin    string `json:"coin"`    // required only when it resolves to an upgrade
	Cancel  bool   `json:"cancel"`  // clear a pending scheduled downgrade
}

// HandleAccountChange handles mid-cycle plan changes from the settings section.
func (s *Service) HandleAccountChange(getUID func(*http.Request) int64) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uid := getUID(r)
		if uid == 0 {
			http.Error(w, `{"error":"unauthorized"}`, 401)
			return
		}
		var b changeReq
		if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
			http.Error(w, `{"error":"bad request"}`, 400)
			return
		}
		ctx := r.Context()

		// Cancel a pending scheduled change.
		if b.Cancel {
			_, err := s.pool.Exec(ctx, `UPDATE users SET pending_tier=NULL, pending_storage_bytes=NULL, updated_at=now() WHERE id=$1`, uid)
			if err != nil {
				http.Error(w, `{"error":"server error"}`, 500)
				return
			}
			writeJSON(w, map[string]any{"ok": true, "canceled": true})
			return
		}

		if _, ok := monthlyCents[b.Tier]; !ok {
			http.Error(w, `{"error":"invalid tier"}`, 400)
			return
		}
		if _, ok := storageCents(b.Storage, "monthly"); !ok {
			http.Error(w, `{"error":"invalid storage option"}`, 400)
			return
		}
		targetBytes := storageBytesFor(b.Storage)

		// Load current plan.
		var curTier string
		var curExtra int64
		var paidUntil *time.Time
		err := s.pool.QueryRow(ctx, `SELECT tier, storage_extra_bytes, paid_until FROM users WHERE id=$1`, uid).
			Scan(&curTier, &curExtra, &paidUntil)
		if err != nil {
			http.Error(w, `{"error":"server error"}`, 500)
			return
		}
		if paidUntil == nil || !paidUntil.After(time.Now()) {
			http.Error(w, `{"error":"no active subscription — use checkout to subscribe or renew","type":"no_active_sub"}`, 400)
			return
		}

		// Period from most recent applied subscription payment; default monthly
		// (covers admin-comped accounts that never paid through the provider).
		period, periodDays := "monthly", 30
		var p string
		if err := s.pool.QueryRow(ctx, `
			SELECT COALESCE(period,'monthly') FROM payments
			WHERE user_id=$1 AND kind='subscription' AND status='applied'
			ORDER BY id DESC LIMIT 1`, uid).Scan(&p); err == nil && p == "yearly" {
			period, periodDays = "yearly", 365
		}

		// Compare full-period prices.
		curT, _ := tierCents(curTier, period)
		curS, _ := storageCents(storagePackForBytes(curExtra), period)
		tgtT, _ := tierCents(b.Tier, period)
		tgtS, _ := storageCents(b.Storage, period)
		delta := (tgtT + tgtS) - (curT + curS)

		// No change vs current and nothing higher -> treat as cancel of any pending.
		if b.Tier == curTier && targetBytes == curExtra {
			_, _ = s.pool.Exec(ctx, `UPDATE users SET pending_tier=NULL, pending_storage_bytes=NULL, updated_at=now() WHERE id=$1`, uid)
			writeJSON(w, map[string]any{"ok": true, "note": "already on this plan", "scheduled": false})
			return
		}

		// Downgrade / lateral -> schedule for next renewal (free).
		if delta <= 0 {
			_, err := s.pool.Exec(ctx, `UPDATE users SET pending_tier=$2, pending_storage_bytes=$3, updated_at=now() WHERE id=$1`,
				uid, b.Tier, targetBytes)
			if err != nil {
				http.Error(w, `{"error":"server error"}`, 500)
				return
			}
			writeJSON(w, map[string]any{
				"ok":           true,
				"scheduled":    true,
				"effective_at": paidUntil,
				"tier":         b.Tier,
				"storage":      b.Storage,
			})
			return
		}

		// Upgrade -> prorated charge for the remaining days, $1 floor.
		if b.Coin == "" {
			http.Error(w, `{"error":"coin required"}`, 400)
			return
		}
		daysRemaining := int(math.Ceil(time.Until(*paidUntil).Hours() / 24))
		if daysRemaining < 1 {
			daysRemaining = 1
		}
		prorated := (delta*int64(daysRemaining) + int64(periodDays) - 1) / int64(periodDays) // ceil
		if prorated < 100 {
			prorated = 100 // $1 floor (avoid dust the provider may reject)
		}

		payID, pd, err := s.createPaymentRow(ctx, uid, "upgrade", b.Tier, period, 0, 0, targetBytes, prorated, b.Coin)
		if err != nil {
			log.Printf("pay: upgrade create: %v", err)
			http.Error(w, `{"error":"payment provider error"}`, 502)
			return
		}
		writeJSON(w, map[string]any{
			"ok":             true,
			"upgrade":        true,
			"payment_id":     payID,
			"pay_address":    pd.PayAddress,
			"pay_amount":     pd.PayAmount,
			"pay_currency":   pd.PayCurrency,
			"price_usd":      float64(prorated) / 100.0,
			"prorated":       true,
			"days_remaining": daysRemaining,
		})
	}
}

type paymentData struct {
	PaymentID   string
	PayAddress  string
	PayAmount   float64
	PayCurrency string
}

func (s *Service) npCreatePayment(ctx context.Context, orderID string, cents int64, coin string) (*paymentData, error) {
	body := map[string]any{
		"price_amount":     float64(cents) / 100.0,
		"price_currency":   "usd",
		"pay_currency":     coin,
		"order_id":         orderID,
		"ipn_callback_url": s.baseURL + "/pay/ipn",
	}
	jb, _ := json.Marshal(body)
	req, _ := http.NewRequestWithContext(ctx, "POST", "https://api.nowpayments.io/v1/payment", bytes.NewReader(jb))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", s.npAPIKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("nowpayments status %d", resp.StatusCode)
	}
	var out struct {
		PaymentID   json.Number `json:"payment_id"`
		PayAddress  string      `json:"pay_address"`
		PayAmount   float64     `json:"pay_amount"`
		PayCurrency string      `json:"pay_currency"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &paymentData{
		PaymentID:   out.PaymentID.String(),
		PayAddress:  out.PayAddress,
		PayAmount:   out.PayAmount,
		PayCurrency: out.PayCurrency,
	}, nil
}

func (s *Service) HandleStatus(getUID func(*http.Request) int64) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uid := getUID(r)
		if uid == 0 {
			http.Error(w, `{"error":"unauthorized"}`, 401)
			return
		}
		id := r.URL.Query().Get("id")
		var status string
		err := s.pool.QueryRow(r.Context(),
			`SELECT status FROM payments WHERE id=$1 AND user_id=$2`, id, uid).Scan(&status)
		if err != nil {
			http.Error(w, `{"error":"not found"}`, 404)
			return
		}
		writeJSON(w, map[string]any{"status": status})
	}
}

// ---- IPN webhook (provider -> us) ----

func (s *Service) HandleIPN(w http.ResponseWriter, r *http.Request) {
	sig := r.Header.Get("x-nowpayments-sig")
	raw := readAll(r)
	if !s.verifyNPSig(raw, sig) {
		log.Printf("pay: IPN bad signature")
		http.Error(w, "bad sig", 401)
		return
	}
	var ipn struct {
		PaymentID     json.Number `json:"payment_id"`
		OrderID       string      `json:"order_id"`
		PaymentStatus string      `json:"payment_status"`
		PayCurrency   string      `json:"pay_currency"`
	}
	if err := json.Unmarshal(raw, &ipn); err != nil {
		http.Error(w, "bad body", 400)
		return
	}
	idStr := strings.TrimPrefix(ipn.OrderID, "gp_")
	payID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "bad order", 400)
		return
	}
	ctx := r.Context()
	_, _ = s.pool.Exec(ctx, `
		UPDATE payments SET provider_pid=$2, status=$3, pay_currency=COALESCE(NULLIF($4,''),pay_currency), updated_at=now()
		WHERE id=$1`, payID, ipn.PaymentID.String(), ipn.PaymentStatus, ipn.PayCurrency)

	if ipn.PaymentStatus == "finished" || ipn.PaymentStatus == "confirmed" {
		if err := s.applyPayment(ctx, payID); err != nil {
			log.Printf("pay: apply %d: %v", payID, err)
			http.Error(w, "apply error", 500)
			return
		}
	}
	w.WriteHeader(200)
	w.Write([]byte("ok"))
}

// applyPayment grants the purchase. Idempotent (marks 'applied').
func (s *Service) applyPayment(ctx context.Context, payID int64) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var userID int64
	var kind, tier, period string
	var days int
	var budgetMicros, storageBytes int64
	var status string
	err = tx.QueryRow(ctx, `
		SELECT user_id, kind, COALESCE(tier,''), COALESCE(period,''), days, budget_micros, storage_bytes, status
		FROM payments WHERE id=$1 FOR UPDATE`, payID).
		Scan(&userID, &kind, &tier, &period, &days, &budgetMicros, &storageBytes, &status)
	if err != nil {
		return err
	}
	if status == "applied" {
		return tx.Commit(ctx) // idempotent no-op
	}

	switch kind {
	case "subscription":
		var monthly int64
		_ = tx.QueryRow(ctx, `SELECT budget_micros FROM tier_defaults WHERE tier=$1`, tier).Scan(&monthly)
		// Bundled + recurring: tier and storage both set from THIS payment; any
		// scheduled downgrade (pending_*) is cleared (payment is the new truth).
		_, err = tx.Exec(ctx, `
			UPDATE users SET
			  tier = $2,
			  paid_until = GREATEST(now(), COALESCE(paid_until, now())) + ($3 || ' days')::interval,
			  monthly_budget_micros = $4,
			  compute_budget_micros = $4,
			  compute_used_micros = 0,
			  compute_budget_cents = $4/10000,
			  compute_used_cents = 0,
			  budget_reset = now() + interval '30 days',
			  storage_extra_bytes = $5,
			  pending_tier = NULL,
			  pending_storage_bytes = NULL,
			  updated_at = now()
			WHERE id=$1`, userID, tier, strconv.Itoa(days), monthly, storageBytes)
		if err != nil {
			return err
		}
	case "upgrade":
		// Mid-cycle upgrade: set new tier + storage NOW. Do NOT extend paid_until,
		// do NOT reset budget_reset. Raise the budget ceiling to the new tier's
		// allowance (GREATEST preserves any active top-up) but NEVER reset
		// compute_used_micros — that closes the burn-then-upgrade-for-fresh-budget
		// exploit. Clears any pending scheduled change.
		var newAllowance int64
		_ = tx.QueryRow(ctx, `SELECT budget_micros FROM tier_defaults WHERE tier=$1`, tier).Scan(&newAllowance)
		_, err = tx.Exec(ctx, `
			UPDATE users SET
			  tier = $2,
			  storage_extra_bytes = $3,
			  monthly_budget_micros = $4,
			  compute_budget_micros = GREATEST(compute_budget_micros, $4),
			  compute_budget_cents = GREATEST(compute_budget_micros, $4)/10000,
			  pending_tier = NULL,
			  pending_storage_bytes = NULL,
			  updated_at = now()
			WHERE id=$1`, userID, tier, storageBytes, newAllowance)
		if err != nil {
			return err
		}
	case "topup":
		_, err = tx.Exec(ctx, `
			UPDATE users SET
			  compute_budget_micros = compute_budget_micros + $2,
			  compute_budget_cents = (compute_budget_micros + $2)/10000,
			  updated_at = now()
			WHERE id=$1`, userID, budgetMicros)
		if err != nil {
			return err
		}
	}

	_, err = tx.Exec(ctx, `UPDATE payments SET status='applied', updated_at=now() WHERE id=$1`, payID)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// verifyNPSig: NOWPayments signs the JSON body (keys sorted) with HMAC-SHA512.
func (s *Service) verifyNPSig(raw []byte, sig string) bool {
	if s.npIPNSecret == "" || sig == "" {
		return false
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return false
	}
	sorted := sortedJSON(obj)
	mac := hmac.New(sha512.New, []byte(s.npIPNSecret))
	mac.Write([]byte(sorted))
	expected := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(sig))
}

func sortedJSON(v any) string {
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var b strings.Builder
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteByte(',')
			}
			kb, _ := json.Marshal(k)
			b.Write(kb)
			b.WriteByte(':')
			b.WriteString(sortedJSON(t[k]))
		}
		b.WriteByte('}')
		return b.String()
	case []any:
		var b strings.Builder
		b.WriteByte('[')
		for i, e := range t {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(sortedJSON(e))
		}
		b.WriteByte(']')
		return b.String()
	default:
		jb, _ := json.Marshal(v)
		return string(jb)
	}
}

// ---- helpers ----

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func readAll(r *http.Request) []byte {
	const max = 1 << 20
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 4096)
	for len(buf) < max {
		n, err := r.Body.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if err != nil {
			break
		}
	}
	return buf
}
