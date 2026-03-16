package malipo

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/brandon-kigen/malipo/auth"
	"github.com/brandon-kigen/malipo/callback"
	"github.com/brandon-kigen/malipo/session"
	"github.com/brandon-kigen/malipo/store/sqlite"
	"github.com/brandon-kigen/malipo/x402"
)

type closer interface {
	Close() error
}

type Config struct {
	ConsumerKey      string
	ConsumerSecret   string
	Environment      auth.Environment
	Shortcode        string
	Passkey          string
	CallbackURL      string
	AccountReference string
	TransactionDesc  string
	TTL              time.Duration
	QueryThreshold   time.Duration
	DBPath           string
}

type GateOptions struct {
	Amount         int64
	Currency       string
	Description    string
	PhoneExtractor func(r *http.Request) (string, error)
}

type Malipo struct {
	Manager   *session.Manager
	shortcode string
	db        closer
}

func New(ctx context.Context, cfg Config) (*Malipo, error) {
	if cfg.ConsumerKey == "" {
		return nil, fmt.Errorf("malipo: ConsumerKey is required")
	}
	if cfg.ConsumerSecret == "" {
		return nil, fmt.Errorf("malipo: ConsumerSecret is required")
	}
	if cfg.Shortcode == "" {
		return nil, fmt.Errorf("malipo: Shortcode is required")
	}
	if cfg.Passkey == "" {
		return nil, fmt.Errorf("malipo: Passkey is required")
	}
	if cfg.CallbackURL == "" {
		return nil, fmt.Errorf("malipo: CallbackURL is required")
	}
	if cfg.DBPath == "" {
		cfg.DBPath = "./malipo.db"
	}

	db, err := sqlite.NewSQLiteAdapter(ctx, cfg.DBPath)
	if err != nil {
		return nil, fmt.Errorf("malipo: storage init failed: %w", err)
	}

	authManager := auth.NewManager(auth.Config{ConsumerKey: cfg.ConsumerKey, ConsumerSecret: cfg.ConsumerSecret, Environment: cfg.Environment})

	manager := session.NewManager(authManager, db, session.Config{
		Shortcode:        cfg.Shortcode,
		Passkey:          cfg.Passkey,
		CallbackURL:      cfg.CallbackURL,
		AccountReference: cfg.AccountReference,
		TransactionDesc:  cfg.TransactionDesc,
		TTL:              cfg.TTL,
		QueryThreshold:   cfg.QueryThreshold,
	})

	return &Malipo{
		Manager:   manager,
		shortcode: cfg.Shortcode,
		db:        db,
	}, nil
}

func (m *Malipo) CallbackHandler() http.Handler {
	return callback.NewHandler(callback.HandlerConfig{
		Manager: m.Manager,
	})
}

func (m *Malipo) Gate(opts GateOptions) func(http.Handler) http.Handler {
	return x402.Gate(x402.GateOptions{
		Amount:         opts.Amount,
		Currency:       opts.Currency,
		Description:    opts.Description,
		PhoneExtractor: opts.PhoneExtractor,
		Shortcode:      m.shortcode, // ← injected
		Manager:        m.Manager,   // ← injected
	})
}

func (m *Malipo) Shutdown() error {
	m.Manager.Stop()
	if m.db != nil {
		return m.db.Close()
	}
	return nil
}
