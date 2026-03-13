package sqlite

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
	"strings"
	"time"

	"github.com/brandon-kigen/malipo/store"
	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schema string

//go:embed queries/create_session.sql
var createSessionQuery string

//go:embed queries/get_session.sql
var getSessionQuery string

//go:embed queries/get_by_checkout_id.sql
var getByCheckoutIDQuery string

//go:embed queries/transition.sql
var transitionQuery string

//go:embed queries/consume_if_confirmed.sql
var consumeIfConfirmedQuery string

//go:embed queries/expire_stale.sql
var expireStaleQuery string

// SQLiteAdapter is a persistent implementation of store.StorageAdapter
// backed by a local SQLite database via modernc.org/sqlite (pure Go, no CGO).
// It is the default adapter for production deployments.
//
// All methods are safe for concurrent use — database/sql manages a connection
// pool internally. WAL mode is enabled at schema init time to allow concurrent
// reads during writes.
type SQLiteAdapter struct {
	db *sql.DB
}

// Compile-time check that *SQLiteAdapter satisfies store.StorageAdapter.
var _ store.StorageAdapter = (*SQLiteAdapter)(nil)

// NewSQLiteAdapter opens a SQLite database at the given path, verifies
// connectivity, and initialises the schema if it does not already exist.
//
// Pass ":memory:" as dbPath for an in-memory database suitable for
// integration tests — state is not persisted across connections.
//
// Returns an error if the database cannot be opened, pinged, or initialised.
func NewSQLiteAdapter(ctx context.Context, dbPath string) (*SQLiteAdapter, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("sqlite: open failed: %w", err)
	}

	if err := db.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("sqlite: ping failed: %w", err)
	}

	_, err = db.ExecContext(ctx, schema)
	if err != nil {
		return nil, fmt.Errorf("sqlite: schema init failed: %w", err)
	}

	return &SQLiteAdapter{db: db}, nil
}

// Close releases the underlying database connection pool.
// Call this in your server shutdown handler — typically via defer or
// signal trapping alongside session.Manager.Stop().
func (a *SQLiteAdapter) Close() error {
	return a.db.Close()
}

// Create inserts a new session record into the database.
// Returns store.ErrSessionExists if a session with the same ID already exists.
// Timestamps are stored as RFC3339 strings — SQLite has no native datetime type.
// Nil pointer fields are stored as SQL NULL.
func (a *SQLiteAdapter) Create(ctx context.Context, s *store.Session) error {
	// ConsumedAt is *time.Time — must be formatted to RFC3339 string or NULL.
	// Passing *time.Time directly would not produce a valid TEXT value in SQLite.
	var consumedAt *string
	if s.ConsumedAt != nil {
		t := s.ConsumedAt.Format(time.RFC3339)
		consumedAt = &t
	}

	_, err := a.db.ExecContext(ctx, createSessionQuery,
		s.ID,
		string(s.State),
		s.Phone,
		s.Amount,
		s.Currency,
		s.Shortcode,
		s.CheckoutRequestID,
		s.MerchantRequestID,
		s.MpesaReceiptNumber,
		s.ConfirmedAmount,
		s.ConfirmedPhone,
		s.CreatedAt.UTC().Format(time.RFC3339),
		s.ExpiresAt.UTC().Format(time.RFC3339),
		consumedAt,
	)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return store.ErrSessionExists
		}
		return fmt.Errorf("sqlite: create failed: %w", err)
	}

	return nil
}

// Get returns the session with the given ID.
// Returns store.ErrNotFound if no session exists with that ID.
func (a *SQLiteAdapter) Get(ctx context.Context, id string) (*store.Session, error) {
	rows, err := a.db.QueryContext(ctx, getSessionQuery, id)
	if err != nil {
		return nil, fmt.Errorf("sqlite: get failed: %w", err)
	}
	defer rows.Close()

	if !rows.Next() {
		return nil, store.ErrNotFound
	}

	return scanSession(rows)
}

// GetByCheckoutID returns the session with the given Daraja CheckoutRequestID.
// Returns store.ErrNotFound if no session is indexed under that ID.
func (a *SQLiteAdapter) GetByCheckoutID(ctx context.Context, checkoutID string) (*store.Session, error) {
	rows, err := a.db.QueryContext(ctx, getByCheckoutIDQuery, checkoutID)
	if err != nil {
		return nil, fmt.Errorf("sqlite: get by checkout id failed: %w", err)
	}
	defer rows.Close()

	if !rows.Next() {
		return nil, store.ErrNotFound
	}

	return scanSession(rows)
}

// Transition atomically advances a session from one state to another.
// The WHERE clause guards on both id and from-state — if the session
// has already moved to a different state, rows affected will be zero
// and ErrInvalidTransition is returned.
// Optional update fields use COALESCE — nil means keep the existing value.
func (a *SQLiteAdapter) Transition(ctx context.Context, id string, from, to store.State, u *store.Update) error {
	var (
		checkoutRequestID  *string
		merchantRequestID  *string
		mpesaReceiptNumber *string
		confirmedAmount    *int64
		confirmedPhone     *string
		consumedAt         *string
	)

	if u != nil {
		checkoutRequestID = u.CheckoutRequestID
		merchantRequestID = u.MerchantRequestID
		mpesaReceiptNumber = u.MpesaReceiptNumber
		confirmedAmount = u.ConfirmedAmount
		confirmedPhone = u.ConfirmedPhone
		if u.ConsumedAt != nil {
			t := u.ConsumedAt.UTC().Format(time.RFC3339)
			consumedAt = &t
		}
	}

	result, err := a.db.ExecContext(ctx, transitionQuery,
		// SET clause — new state and COALESCE optional fields
		string(to),
		checkoutRequestID,
		merchantRequestID,
		mpesaReceiptNumber,
		confirmedAmount,
		confirmedPhone,
		consumedAt,
		// WHERE clause — id and from-state guard
		id,
		string(from),
	)
	if err != nil {
		return fmt.Errorf("sqlite: transition failed: %w", err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("sqlite: rows affected failed: %w", err)
	}

	if affected == 0 {
		return store.ErrInvalidTransition
	}

	return nil
}

// ConsumeIfConfirmed atomically transitions a session from CONFIRMED to CONSUMED.
// This is the double-spend prevention mechanism — the WHERE state = 'CONFIRMED'
// clause ensures only one concurrent caller can succeed.
//
// If rows affected is zero the session either does not exist or was not CONFIRMED.
// A follow-up Get distinguishes between not found and already consumed.
func (a *SQLiteAdapter) ConsumeIfConfirmed(ctx context.Context, id string) (*store.Session, error) {
	consumedAt := time.Now().UTC().Format(time.RFC3339)

	result, err := a.db.ExecContext(ctx, consumeIfConfirmedQuery,
		consumedAt,
		id,
	)
	if err != nil {
		return nil, fmt.Errorf("sqlite: consume failed: %w", err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("sqlite: rows affected failed: %w", err)
	}

	if affected == 0 {
		// Fetch to distinguish not found from already consumed.
		session, err := a.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		if session.State == store.StateConsumed {
			return nil, store.ErrAlreadyConsumed
		}
		return nil, store.ErrInvalidTransition
	}

	return a.Get(ctx, id)
}

// ExpireStale transitions all non-terminal sessions whose expires_at is
// before the given cutoff to TIMEOUT.
// Returns the number of sessions expired. Zero is not an error.
func (a *SQLiteAdapter) ExpireStale(ctx context.Context, before time.Time) (int64, error) {
	result, err := a.db.ExecContext(ctx, expireStaleQuery,
		before.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return 0, fmt.Errorf("sqlite: expire stale failed: %w", err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("sqlite: rows affected failed: %w", err)
	}

	return affected, nil
}

// scanSession scans a single row from a sessions query into a store.Session.
// Used by both Get and GetByCheckoutID to avoid duplicating scan logic.
// Timestamps are parsed from RFC3339 strings. Nullable columns are scanned
// into pointer types — nil means the column was NULL.
func scanSession(rows *sql.Rows) (*store.Session, error) {
	var (
		session   store.Session
		state     string
		createdAt string
		expiresAt string

		// nullable columns — scanned as pointers, NULL becomes nil
		checkoutRequestID  *string
		merchantRequestID  *string
		mpesaReceiptNumber *string
		confirmedAmount    *int64
		confirmedPhone     *string
		consumedAt         *string
	)

	err := rows.Scan(
		&session.ID,
		&state,
		&session.Phone,
		&session.Amount,
		&session.Currency,
		&session.Shortcode,
		&checkoutRequestID,
		&merchantRequestID,
		&mpesaReceiptNumber,
		&confirmedAmount,
		&confirmedPhone,
		&createdAt,
		&expiresAt,
		&consumedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("sqlite: scan failed: %w", err)
	}

	session.State = store.State(state)

	session.CreatedAt, err = time.Parse(time.RFC3339, createdAt)
	if err != nil {
		return nil, fmt.Errorf("sqlite: parse created_at failed: %w", err)
	}

	session.ExpiresAt, err = time.Parse(time.RFC3339, expiresAt)
	if err != nil {
		return nil, fmt.Errorf("sqlite: parse expires_at failed: %w", err)
	}

	if checkoutRequestID != nil {
		session.CheckoutRequestID = *checkoutRequestID
	}
	if merchantRequestID != nil {
		session.MerchantRequestID = *merchantRequestID
	}
	if mpesaReceiptNumber != nil {
		session.MpesaReceiptNumber = *mpesaReceiptNumber
	}
	if confirmedAmount != nil {
		session.ConfirmedAmount = confirmedAmount
	}
	if confirmedPhone != nil {
		session.ConfirmedPhone = confirmedPhone
	}
	if consumedAt != nil {
		t, err := time.Parse(time.RFC3339, *consumedAt)
		if err != nil {
			return nil, fmt.Errorf("sqlite: parse consumed_at failed: %w", err)
		}
		session.ConsumedAt = &t
	}

	return &session, nil
}
