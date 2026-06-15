package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"gramsai/db"
	"gramsai/internal/auth"
	"gramsai/internal/pay"
	"gramsai/internal/memory"
	"gramsai/internal/proxy"
	"gramsai/internal/router"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pressly/goose/v3"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func main() {
	listenAddr := envOr("API_LISTEN", "unix:/run/gramsai/api.sock")
	dbURL := envOr("DATABASE_URL", "")
	if dbURL == "" {
		log.Fatal("DATABASE_URL is required")
	}

	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "migrate":
			runMigrations(dbURL)
			return
		case "mint-token":
			mintToken(dbURL, os.Args)
			return
		}
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		log.Fatalf("DB connect failed: %v", err)
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		log.Fatalf("DB ping failed: %v", err)
	}
	log.Println("PostgreSQL connected")
	runMigrations(dbURL)

	// GRAMSAI_DEK_MAIN: keystore created before auth so login can push DEKs to Redis.
	memKeys, kerr := memory.NewKeyStore(
		envOr("REDIS_ADDR", "127.0.0.1:6379"),
		envOr("REDIS_PASSWORD", ""),
		30*24*time.Hour, // matches sessionTTL
		pool,
	)
	if kerr != nil {
		log.Printf("memory keystore: Redis unavailable (%v) — memory features inactive", kerr)
	} else {
		log.Println("memory keystore: Redis connected")
		memKeys.StartSweeper(1 * time.Hour) // failsafe: clear keys of dead sessions
	}
	authSvc := auth.New(pool, memKeys)

	// GRAMSAI_MEM_STORE: encrypted memory store + Redis fact-cache (all 3 tiers).
	// Consumed by proxy injection + the extraction worker (1e).
	memStore := memory.NewStore(pool, memKeys)
	// GRAMSAI_MEM_EXTRACTOR: background worker — flash extraction, 24h-gated, active
	// sessions only. One flash call/user/day, builds the memory profile.
	if kerr == nil {
		memStore.StartExtractor(envOr("OPENROUTER_API_KEY", ""))
		// GRAMSAI_EPISODE_WORKER: per-conversation episode summaries, every 2h.
		memStore.StartEpisodeWorker(envOr("OPENROUTER_API_KEY", ""))
		log.Println("memory episode worker: started")
		log.Println("memory extractor: started")
	}

	rtr := router.New(pool, envOr("AGENT_SECRET", ""), memKeys)
	// Reaper: stop containers idle > 30 min, check every 5 min. Respawn on return.
	rtr.StartReaper(30*time.Minute, 5*time.Minute)
	rtr.StartStoragePoller(5 * time.Minute)
	rtr.StartLifecycle(7, 24*time.Hour)

	// getUID resolves the session cookie to a user id for the router.
	getUID := func(req *http.Request) int64 {
		c, err := req.Cookie(auth.CookieName)
		if err != nil {
			return 0
		}
		uid, err := authSvc.Resolve(req.Context(), c.Value)
		if err != nil {
			return 0
		}
		return uid
	}
	ocRouter := rtr.Proxy(getUID)

	// Payment service (provider-agnostic; NOWPayments first adapter).
	paySvc := pay.New(pool,
		envOr("NOWPAYMENTS_API_KEY", ""),
		envOr("NOWPAYMENTS_IPN_SECRET", ""),
		envOr("PUBLIC_BASE_URL", "https://grams.chat"))

	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	r.Use(middleware.RealIP)

	r.Get("/api/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"status":"ok","time":"%s"}`, time.Now().UTC().Format(time.RFC3339))
	})

	// --- Auth endpoints (username+password signup, sessions) ---
	r.Post("/auth/register", authSvc.HandleRegister)
	r.Post("/auth/login", authSvc.HandleLogin)
	r.Post("/auth/logout", authSvc.HandleLogout)
	r.Get("/auth/me", authSvc.HandleMe)
	r.Get("/login", authSvc.LoginPage)
	r.Get("/welcome", authSvc.LandingPage)
	r.Get("/welcome", authSvc.LandingPage)
	r.Get("/welcome", authSvc.LandingPage)
	r.Get("/auth/check", authSvc.HandleCheck)
	r.Get("/auth/paid-check", authSvc.HandlePaidCheck)

	// --- Payment endpoints ---
	// /pay/create is authed (session cookie -> user). /pay/ipn is the public
	// provider webhook, authenticated by HMAC inside the handler (no cookie).
	r.Post("/pay/create", paySvc.HandleCreate(getUID))
	r.Post("/pay/ipn", paySvc.HandleIPN)
	r.Get("/pay/status", paySvc.HandleStatus(getUID))
	r.Get("/subscribe", paySvc.SubscribePage)
	r.Get("/pay/coins", paySvc.HandleCoins)
	r.Post("/account/change", paySvc.HandleAccountChange(getUID))
	r.Get("/account/info", authSvc.HandleAccountInfo)
	r.Get("/account/connections", authSvc.HandleConnections)
	r.Get("/account/payments", authSvc.HandleUserPayments)
	r.Get("/account/tokens", authSvc.HandleListTokens)
	r.Post("/account/tokens", authSvc.HandleCreateToken)
	r.Post("/account/tokens/revoke", authSvc.HandleRevokeToken)
	r.Post("/account/password", authSvc.HandleChangePassword)
	r.Get("/account/sessions", authSvc.HandleListSessions)
	r.Post("/account/sessions/revoke", authSvc.HandleRevokeSession)
	r.Post("/account/sessions/revoke-others", authSvc.HandleRevokeOtherSessions)
	r.Post("/auth/login/totp", authSvc.HandleLoginTOTP)
	r.Post("/auth/reset", authSvc.HandleResetPassword)
	r.Get("/auth/oauth/{provider}/start", authSvc.HandleOAuthStart)
	r.Get("/auth/oauth/{provider}/callback", authSvc.HandleOAuthCallback)
	r.Post("/account/2fa/setup", authSvc.HandleTOTPSetup)
	r.Post("/account/2fa/enable", authSvc.HandleTOTPEnable)
	r.Post("/account/2fa/disable", authSvc.HandleTOTPDisable)
	r.Get("/account/2fa/status", authSvc.HandleTOTPStatus)
	r.Post("/account/delete", rtr.HandleDeleteAccount(getUID))
	// memory CRUD (Settings > Security & Privacy) — cookie-authed like other /account/*.
	{
		memCRUD := memStore.CRUDHandler(getUID)
		r.Get("/account/memory", memCRUD)
		r.Post("/account/memory", memCRUD)
		r.Get("/account/memory/status", memCRUD)
		r.Post("/account/memory/toggle", memCRUD)
		r.Patch("/account/memory/{id}", memCRUD)
		r.Delete("/account/memory/{id}", memCRUD)
	}
	// data export + delete-all-chats (Privacy & Security, Step 2)
	{
		dataH := memStore.DataHandler(getUID, rtr.WipeChats)
		r.Get("/account/export", dataH)
		r.Post("/account/chats/delete-all", dataH)
	}

	// OpenAI-compatible LLM gateway: containers' OpenCode points here.
	// Holds the shared OpenRouter key; meters per-user cost; maps alias->real model.
	r.Get("/dl", rtr.HandleDownload(getUID))
	// GRAMSAI live browser panel proxy -> per-user container bridge :8088.
	// /browse + /healthz are plain HTTP; /ws/* are WebSocket (hijack-and-pipe).
	r.Get("/api/browser/healthz", rtr.HandleBrowser(getUID))
	r.Post("/api/browser/browse", rtr.HandleBrowser(getUID))
	r.Get("/api/browser/ws/stream", rtr.HandleBrowser(getUID))
	r.Get("/api/browser/ws/control", rtr.HandleBrowser(getUID))
	r.Post("/v1/chat/completions", proxy.LLMHandler(pool, memStore, envOr("OPENROUTER_API_KEY", "")))
	// GRAMSAI_MEM_SEARCH: container's search_memory tool -> vector search over episodes.
	r.Post("/api/memory/search", memStore.SearchHandler(pool, envOr("OPENROUTER_API_KEY", ""), proxy.ResolveUID))

	// File download endpoint
	r.Get("/api/file/dl/*", func(w http.ResponseWriter, r *http.Request) {
		filePath := strings.TrimPrefix(r.URL.Path, "/api/file/dl/")
		if filePath == "" {
			http.Error(w, "no path", 400)
			return
		}
		ocURL := envOr("OPENCODE_URL", "http://10.152.152.100:5002")
		resp, err := http.Get(ocURL + "/file/content?directory=/workspace&path=" + filePath)
		if err != nil {
			http.Error(w, "fetch error", 502)
			return
		}
		defer resp.Body.Close()
		type fileResp struct {
			Type    string `json:"type"`
			Content string `json:"content"`
		}
		var result fileResp
		json.NewDecoder(resp.Body).Decode(&result)
		w.Header().Set("Content-Disposition", "attachment; filename=\""+filePath+"\"")
		w.Header().Set("Content-Type", "application/octet-stream")
		fmt.Fprint(w, result.Content)
	})

	// Per-user OpenCode routing: every frontend path group is proxied to the
	// authenticated user's own container (spawned on demand). Replaces the old
	// static single-container proxy.
	ocPaths := []string{
		"/agent", "/command", "/config", "/event", "/experimental", "/file",
		"/find", "/formatter", "/global", "/instance", "/log", "/lsp", "/mcp",
		"/path", "/permission", "/project", "/provider", "/pty", "/question",
		"/session", "/skill", "/sync", "/tui", "/vcs",
	}
	for _, p := range ocPaths {
		r.Handle(p, ocRouter)
		r.Handle(p+"/*", ocRouter)
	}

	listener, err := makeListener(listenAddr)
	if err != nil {
		log.Fatalf("Listen failed: %v", err)
	}

	srv := &http.Server{Handler: r}
	go func() {
		log.Printf("GramsAI API listening on %s", listenAddr)
		if err := srv.Serve(listener); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server error: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("Shutting down...")
	ctx2, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	srv.Shutdown(ctx2)
	log.Println("Done")
}

func mintToken(dbURL string, args []string) {
	if len(args) < 3 {
		log.Fatal("usage: api mint-token <user_id>")
	}
	userID := args[2]
	tok := "gsk-" + randHex(32)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	defer pool.Close()
	ct, err := pool.Exec(ctx, `UPDATE users SET api_token=$2 WHERE id=$1`, userID, tok)
	if err != nil {
		log.Fatalf("update: %v", err)
	}
	if ct.RowsAffected() == 0 {
		log.Fatalf("no user with id=%s", userID)
	}
	fmt.Printf("user %s token: %s\n", userID, tok)
}

func randHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func runMigrations(dbURL string) {
	sqlDB, err := goose.OpenDBWithDriver("pgx", dbURL)
	if err != nil {
		log.Fatalf("Goose open DB: %v", err)
	}
	defer sqlDB.Close()
	goose.SetBaseFS(db.Migrations)
	if err := goose.SetDialect("postgres"); err != nil {
		log.Fatalf("Goose dialect: %v", err)
	}
	if err := goose.Up(sqlDB, "migrations"); err != nil {
		log.Fatalf("Goose up: %v", err)
	}
	log.Println("Migrations complete")
}

func makeListener(addr string) (net.Listener, error) {
	if strings.HasPrefix(addr, "unix:") {
		sock := strings.TrimPrefix(addr, "unix:")
		os.Remove(sock)
		ln, err := net.Listen("unix", sock)
		if err != nil {
			return nil, err
		}
		os.Chmod(sock, 0666)
		return ln, nil
	}
	return net.Listen("tcp", addr)
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
