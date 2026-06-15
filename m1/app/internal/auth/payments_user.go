// internal/auth/payments_user.go
//
// A user's own payment history (read-only, scoped to their user_id) PLUS a
// "renewal" hint derived from their most recent subscription payment, so the
// Billing tab can offer one-click quick-renew of the same plan. period is not
// stored on users — the last subscription payment is the source of truth.
package auth

import (
	"net/http"
)

type userPayment struct {
	Kind     string  `json:"kind"`
	Tier     string  `json:"tier"`
	Period   string  `json:"period"`
	PriceUSD float64 `json:"price_usd"`
	Coin     string  `json:"coin"`
	Status   string  `json:"status"`
	Created  string  `json:"created"`
}

// GET /account/payments -> {payments:[...], renewal:{tier,period,storage_bytes}|null}
func (a *Auth) HandleUserPayments(w http.ResponseWriter, r *http.Request) {
	uid, _, ok := a.resolveCaller(w, r)
	if !ok {
		return
	}
	// expire stale waiting invoices (older than 2h) so they don't linger.
	_, _ = a.pool.Exec(r.Context(),
		`UPDATE payments SET status='expired', updated_at=now()
		 WHERE user_id=$1 AND status='waiting' AND created_at < now() - interval '2 hours'`, uid)

	// history shows COMPLETED payments only.
	rows, err := a.pool.Query(r.Context(), `
		SELECT kind, COALESCE(tier,''), COALESCE(period,''),
		       price_usd_cents, COALESCE(pay_currency,''), status, created_at::text
		FROM payments
		WHERE user_id=$1 AND status IN ('finished','applied','confirmed')
		ORDER BY id DESC
		LIMIT 100`, uid)
	out := []userPayment{}
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var p userPayment
			var cents int64
			if rows.Scan(&p.Kind, &p.Tier, &p.Period, &cents, &p.Coin, &p.Status, &p.Created) == nil {
				p.PriceUSD = float64(cents) / 100.0
				out = append(out, p)
			}
		}
	}

	// Renewal hint: the most recent COMPLETED subscription payment's plan.
	var rTier, rPeriod string
	var rStorage int64
	_ = a.pool.QueryRow(r.Context(), `
		SELECT COALESCE(tier,'basic'), COALESCE(period,'monthly'), COALESCE(storage_bytes,0)
		FROM payments
		WHERE user_id=$1 AND kind='subscription'
		  AND status IN ('finished','applied','confirmed')
		ORDER BY id DESC LIMIT 1`, uid).Scan(&rTier, &rPeriod, &rStorage)

	var renewal any
	if rTier != "" {
		renewal = map[string]any{
			"tier":          rTier,
			"period":        rPeriod,
			"storage_bytes": rStorage,
		}
	}

	writeJSON(w, 200, map[string]any{
		"payments": out,
		"renewal":  renewal, // null if they've never had a completed subscription
	})
}
