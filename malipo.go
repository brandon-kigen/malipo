// Package malipo provides a top-level convenience wrapper over the Malipo SDK.
//
// The SDK bridges the x402 HTTP payment protocol to the M-Pesa Daraja STK Push
// API, enabling any net/http handler to be gated behind a real M-Pesa payment
// with minimal configuration.
//
// # Quick start
//
//	m, err := malipo.New(ctx, malipo.Config{
//	    ConsumerKey:    os.Getenv("DARAJA_CONSUMER_KEY"),
//	    ConsumerSecret: os.Getenv("DARAJA_CONSUMER_SECRET"),
//	    Shortcode:      os.Getenv("DARAJA_SHORTCODE"),
//	    Passkey:        os.Getenv("DARAJA_PASSKEY"),
//	    CallbackURL:    "https://yourserver.com/mpesa/callback",
//	})
//	if err != nil {
//	    log.Fatal(err)
//	}
//	defer m.Shutdown()
//
//	mux.Handle("/mpesa/callback", m.CallbackHandler())
//
//	gate := m.Gate(malipo.GateOptions{
//	    Amount: 100,
//	    PhoneExtractor: func(r *http.Request) (string, error) {
//	        return r.Header.Get("X-Phone"), nil
//	    },
//	})
//	mux.Handle("/api/data", gate(yourHandler))
//
// # BYOC model
//
// Malipo is a Bring Your Own Credentials SDK. It runs entirely within the
// developer's own infrastructure — no Malipo servers are in the payment path,
// no user data leaves the developer's server except to Safaricom, and all
// credentials belong to the developer's own Daraja account.
//
// Developers who need finer control over individual components — custom storage
// backends, non-SQLite adapters, or direct session manager access — can bypass
// this package and wire the sub-packages directly:
// auth, session, store/sqlite, x402, and callback.
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

// closer is a private interface that isolates *sqlite.SQLiteAdapter from the
// public Malipo struct. Holding the DB as a closer rather than the concrete
// adapter type means importing malipo does not transitively expose the sqlite
// package to callers who only interact with the top-level API.
type closer interface {
	Close() error
}

// Config holds all configuration required to initialise a Malipo instance.
//
// Required fields: ConsumerKey, ConsumerSecret, Shortcode, Passkey, CallbackURL.
// All other fields are optional — sub-package constructors apply their own
// defaults when zero values are passed through.
//
// Credentials should be sourced from environment variables or a secrets manager,
// not hardcoded. See .env.example in the repository for the expected variable names.
type Config struct {
	// ConsumerKey is the API key from the Safaricom developer portal.
	// Required. Used as the username in Daraja OAuth Basic authentication.
	ConsumerKey string

	// ConsumerSecret is the API secret from the Safaricom developer portal.
	// Required. Used as the password in Daraja OAuth Basic authentication.
	ConsumerSecret string

	// Environment controls which Daraja base URL is targeted.
	// Optional. Defaults to auth.Sandbox when empty.
	// Use auth.Production for live payments.
	Environment auth.Environment

	// Shortcode is the M-Pesa paybill or till number that receives payments.
	// Required. Appears as PayTo in 402 responses and is used to generate
	// STK Push passwords.
	Shortcode string

	// Passkey is the Daraja STK Push passkey for the shortcode.
	// Required. Used to generate the time-bound STK Push password.
	// Found in the Daraja portal under the STK Push section.
	Passkey string

	// CallbackURL is the public HTTPS endpoint where Safaricom will POST
	// STK Push results. Required.
	// Must match the path where CallbackHandler() is mounted.
	// Example: "https://yourserver.com/mpesa/callback"
	CallbackURL string

	// AccountReference is the label shown on the M-Pesa prompt as the
	// payee name or account identifier. Optional.
	// Example: "CompanyX" or "Order #12345"
	AccountReference string

	// TransactionDesc is the human-readable description shown on the
	// M-Pesa PIN prompt. Optional.
	// Example: "Payment for API access"
	TransactionDesc string

	// TTL controls how long a payment session remains valid before it
	// is automatically expired to TIMEOUT state. Optional.
	// Defaults to 90 seconds — matching Safaricom's STK Push timeout.
	TTL time.Duration

	// QueryThreshold controls how long the recovery loop waits before
	// querying Daraja for sessions that have not received a callback.
	// Optional. Defaults to 60 seconds.
	// Sessions older than this threshold are queried via the STK Push
	// Query API on each 30-second recovery tick.
	QueryThreshold time.Duration

	// DBPath is the path to the SQLite database file. Optional.
	// Defaults to "./malipo.db" when empty.
	// Use ":memory:" for tests or ephemeral environments.
	DBPath string
}

// GateOptions configures a single gated route. It is passed to Gate() for
// each handler that requires payment before access is granted.
//
// Manager and Shortcode are intentionally absent — Gate() injects them from
// the Malipo instance. This prevents the caller from accidentally using a
// different manager or shortcode than the one the instance was initialised with.
type GateOptions struct {
	// Amount is the payment amount in whole KES units.
	// Example: 100 for KES 100.
	Amount int64

	// Currency is the ISO 4217 currency code. Optional.
	// Defaults to "KES" when empty. Daraja only supports KES.
	Currency string

	// Description is the human-readable payment description shown on
	// the M-Pesa STK Push prompt on the user's phone.
	Description string

	// PhoneExtractor extracts the payer's M-Pesa phone number from the
	// incoming request. Required — Gate panics at construction time if nil.
	//
	// The implementation is supplied by the developer and may read from any
	// request field appropriate to the application's auth model: a JWT claim,
	// a form value, a session cookie, or a custom header.
	//
	// The returned number is normalised to E.164 internally — common Kenyan
	// formats (0712..., 254712..., +254712...) are all accepted.
	//
	// Returning a non-nil error causes Gate to respond with 400 Bad Request
	// before any payment is attempted.
	PhoneExtractor func(r *http.Request) (string, error)
}

// Malipo is the top-level SDK handle. It wraps the session manager, storage
// adapter, and all sub-package constructors behind a unified API.
//
// Construct with New. Stop background goroutines and release resources
// with Shutdown when the server exits.
type Malipo struct {
	// Manager is the underlying session manager. Exported to allow callers
	// to poll session state via Manager.GetStatus during the async gap between
	// STK Push initiation and Safaricom's callback.
	Manager *session.Manager

	// shortcode is stored separately from Manager because it is needed by
	// Gate to pre-fill x402.GateOptions.Shortcode, and the session manager
	// does not expose its internal config.
	shortcode string

	// db holds the SQLite adapter as a closer so Shutdown can release the
	// connection pool. Stored as an interface to avoid coupling the public
	// struct to the concrete adapter type.
	db closer
}

// New constructs a fully wired Malipo instance.
//
// It validates the five required fields, defaults DBPath to "./malipo.db",
// opens a SQLite database, and wires the auth manager and session manager.
// The session manager starts its background cleanup ticker immediately.
//
// Construction order is intentional: validation and SQLite are attempted before
// any background goroutines are started, so a failed New leaves nothing to clean up.
//
// Returns an error if any required field is empty or if the SQLite database
// cannot be opened or initialised.
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

	authManager := auth.NewManager(auth.Config{
		ConsumerKey:    cfg.ConsumerKey,
		ConsumerSecret: cfg.ConsumerSecret,
		Environment:    cfg.Environment,
	})

	// TTL and QueryThreshold are passed through as-is. session.NewManager
	// applies its own defaults (90s and 60s respectively) when zero values
	// are received — New does not duplicate that logic.
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

// CallbackHandler returns an http.Handler that processes Safaricom STK Push
// callbacks and drives session state transitions.
//
// Mount this handler at the same path registered as CallbackURL in Config:
//
//	mux.Handle("/mpesa/callback", m.CallbackHandler())
//
// The handler always responds 200 — Safaricom does not retry callbacks and
// ignores the response body. Errors after the checkout ID lookup are logged
// and silently absorbed to ensure Safaricom never sees a non-200 response.
//
// Safe to call multiple times — each call returns a new handler backed by
// the same session manager. In practice, mount it once at server startup.
func (m *Malipo) CallbackHandler() http.Handler {
	return callback.NewHandler(callback.HandlerConfig{
		Manager: m.Manager,
	})
}

// Gate returns an x402 payment middleware that gates access to the wrapped
// handler behind a real M-Pesa STK Push payment.
//
// Shortcode and Manager are pre-filled from the Malipo instance. Only
// per-route options (Amount, Currency, Description, PhoneExtractor) are
// supplied by the caller.
//
// Gate panics at construction time — not at request time — if
// opts.PhoneExtractor is nil. This surfaces the misconfiguration immediately
// at server startup rather than on the first request.
//
// Usage:
//
//	gate := m.Gate(malipo.GateOptions{
//	    Amount:      100,
//	    Description: "Access to dataset",
//	    PhoneExtractor: func(r *http.Request) (string, error) {
//	        return r.Header.Get("X-Phone"), nil
//	    },
//	})
//	mux.Handle("/api/data", gate(yourHandler))
func (m *Malipo) Gate(opts GateOptions) func(http.Handler) http.Handler {
	return x402.Gate(x402.GateOptions{
		Amount:         opts.Amount,
		Currency:       opts.Currency,
		Description:    opts.Description,
		PhoneExtractor: opts.PhoneExtractor,
		Shortcode:      m.shortcode,
		Manager:        m.Manager,
	})
}

// Shutdown stops the session manager's background goroutines and releases
// the SQLite connection pool.
//
// Manager.Stop() is called before db.Close() — this ensures the cleanup
// ticker and all per-session TTL goroutines are dead before the database
// connection is released. Reversing the order would allow the cleanup ticker
// to attempt a storage operation on a closed database between the two calls.
//
// Returns the error from db.Close(), which may indicate that the SQLite WAL
// checkpoint did not flush cleanly. The error should be logged even when
// nothing can be done about it.
//
// Call Shutdown in a server shutdown handler, typically via signal trapping:
//
//	c := make(chan os.Signal, 1)
//	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
//	<-c
//	if err := m.Shutdown(); err != nil {
//	    log.Printf("shutdown: %v", err)
//	}
func (m *Malipo) Shutdown() error {
	m.Manager.Stop()
	if m.db != nil {
		return m.db.Close()
	}
	return nil
}