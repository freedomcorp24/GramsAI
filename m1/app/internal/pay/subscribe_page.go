// internal/pay/subscribe_page.go
// White-label subscribe + payment page, served by the gateway (like the login
// page). No NOWPayments branding: the user picks tier/storage/period/coin here,
// we call the Payment API, and render our own address + QR + live-status screen.
package pay

import (
	_ "embed"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"time"
)

//go:embed subscribe.html
var subscribeHTML []byte

// SubscribePage serves the static subscribe UI. Gated by nginx so only
// logged-in users reach it; the page's JS calls /pay/coins, /pay/create, /pay/status.
func (s *Service) SubscribePage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(subscribeHTML)
}

// ---- enabled coin list (proxied + cached) ----
// The subscribe page populates its coin picker from this so we never hardcode
// tickers (the usdcerc20 typo class of bug). The NOWPayments key stays
// server-side; the result is cached for an hour to avoid hammering the API.
var (
	coinsMu     sync.Mutex
	coinsCache  []byte
	coinsExpiry time.Time
)

// HandleCoins returns {"currencies":[...]} — the merchant's enabled pay currencies.
func (s *Service) HandleCoins(w http.ResponseWriter, r *http.Request) {
	coinsMu.Lock()
	if coinsCache != nil && time.Now().Before(coinsExpiry) {
		body := coinsCache
		coinsMu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Write(body)
		return
	}
	coinsMu.Unlock()

	req, _ := http.NewRequestWithContext(r.Context(), "GET", "https://api.nowpayments.io/v1/merchant/coins", nil)
	req.Header.Set("x-api-key", s.npAPIKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		http.Error(w, `{"error":"upstream"}`, 502)
		return
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		http.Error(w, `{"error":"upstream status"}`, 502)
		return
	}
	// merchant/coins returns {"selectedCurrencies":[...]}. Normalize to {"currencies":[...]}.
	var parsed struct {
		Selected []string `json:"selectedCurrencies"`
	}
	_ = json.Unmarshal(raw, &parsed)
	out, _ := json.Marshal(map[string]any{"currencies": parsed.Selected})

	coinsMu.Lock()
	coinsCache = out
	coinsExpiry = time.Now().Add(1 * time.Hour)
	coinsMu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	w.Write(out)
}
