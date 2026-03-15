# Malipo

Go middleware SDK that bridges the [x402 HTTP payment protocol](https://x402.org) to the M-Pesa Daraja STK Push API. Gate any `net/http` resource behind a real M-Pesa payment with just a few lines of code.

---

## Quick Example
```go
// Initialise auth and session manager at startup
authManager := auth.NewManager(auth.Config{
    ConsumerKey:    "your-consumer-key",
    ConsumerSecret: "your-consumer-secret",
    Environment:    auth.Sandbox,
})

adapter, err := sqlite.NewSQLiteAdapter(ctx, "./malipo.db")

manager := session.NewManager(authManager, adapter, session.Config{
    Shortcode:        "174379",
    Passkey:          "your-passkey",
    CallbackURL:      "https://yourserver.com/mpesa/callback",
    AccountReference: "CompanyX",
    TransactionDesc:  "Payment",
})
defer manager.Stop()

// Mount the callback handler at the same path you registered with Daraja
mux.Handle("/mpesa/callback", callback.NewHandler(callback.HandlerConfig{
    Manager: manager,
}))

// Gate any handler
gate := x402.Gate(x402.GateOptions{
    Amount:      100,
    Description: "Access to data",
    Shortcode:   "174379",
    Manager:     manager,
    PhoneExtractor: func(r *http.Request) (string, error) {
        return r.Header.Get("X-Phone"), nil
    },
})

mux.Handle("/api/data", gate(yourHandler))
```

When a client hits `/api/data` without a valid payment proof, Malipo returns a `402 Payment Required` response with the payment requirements and a session ID. The client initiates the STK Push, polls until confirmed, then retries the request with `X-PAYMENT: <sessionId>`. Malipo verifies atomically and releases the resource.

---

## How It Works

### The async gap problem

x402 assumes synchronous payment — client pays, server verifies, resource released in one cycle. M-Pesa STK Push is asynchronous — Safaricom delivers a PIN prompt to the user's SIM over its own network, the user decides, Safaricom posts a callback 5 to 30 seconds later.
```
Client → GET /api/data
Server → 402 Payment Required + session_id (x402 payment requirements body)

         [Safaricom delivers PIN prompt to user's SIM]
         [User enters PIN]
         [Safaricom POSTs callback to your CallbackURL]

Client → GET /status/{session_id}  (polls until CONFIRMED) ← developer implements
Server → 200 CONFIRMED

Client → GET /api/data + X-PAYMENT: <session_id>
Server → 200 OK + data
```

### The solution

Malipo persists a session record that survives the async gap. The session tracks the payment through its full lifecycle via a state machine. The x402 middleware only releases the resource when `ConsumeIfConfirmed` transitions the session from `CONFIRMED` to `CONSUMED` — one atomic operation is the entire double-spend prevention.

### Lost callback recovery

Safaricom does not retry failed callbacks. If your server was unreachable when the callback was posted, the session would be permanently stuck. Malipo's background recovery loop runs every 30 seconds and queries the Daraja STK Push Query API for any session in `STK_PUSHED` or `AWAITING_PIN` that is older than `QueryThreshold` (default 60s). The query result drives the same state machine transitions the callback handler would have fired — the callback is the fast path, the recovery loop is the safety net.

---

## Architecture

Malipo is five packages with a strict one-way dependency chain:
```
x402 Middleware  ─┐
Callback Handler ─┤──► Session Manager ──► TokenProvider (interface)
                  │         │
                  │         ▼
                  │    StorageAdapter (interface)
                  │         │
                  │    ┌────┴─────┐
                  │    ▼          ▼
                  │  SQLite    Memory
                  │  (default) (tests)
                  └──────────────────
```

The Session Manager never imports a concrete storage or auth type. Both are injected as interfaces at construction time — every package is independently testable and the dependency graph is acyclic.

### Packages

| Package | Responsibility |
|---|---|
| `store/` | `StorageAdapter` interface, `Session`, `State`, sentinel errors |
| `session/` | State machine rules, payment orchestration, TTL lifecycle, recovery loop |
| `auth/` | Daraja OAuth token cache, password generation, STK Push HTTP, STK Push Query HTTP |
| `x402/` | x402 scheme types, 402 response writer, Gate middleware |
| `callback/` | Safaricom callback handler, payload validation, metadata extraction |

---

## State Machine

Every payment session moves through a defined set of states. Terminal states cannot be left — any write attempt on a terminal session is rejected by the storage adapter.
```
CREATED → STK_PUSHED → AWAITING_PIN ─┐
               │              │       │
               └──────────────┘       ▼
                      │          CONFIRMED → CONSUMED
                      ▼
                    FAILED
                  CANCELLED
                   TIMEOUT
```

`STK_PUSHED` means Daraja accepted the push request. `AWAITING_PIN` means the STK Push Query API confirmed the prompt was delivered to the user's SIM — the session moves here via the recovery loop when a callback has not yet arrived. Both states exit to the same terminal set. The callback handler and the recovery loop are complementary — whichever fires first wins, and a late arrival on an already-terminal session is silently ignored.

---

## Storage Backends

SQLite is the default — zero configuration, embedded in the binary, no external services required.
```go
// Default — SQLite at the given path
adapter, err := sqlite.NewSQLiteAdapter(ctx, "./malipo.db")

// In-memory SQLite for integration tests
adapter, err := sqlite.NewSQLiteAdapter(ctx, ":memory:")

// In-memory Go map for unit tests
adapter := memory.NewMemoryAdapter()
```

### Bring your own

Implement `store.StorageAdapter` to use any backend:
```go
type StorageAdapter interface {
    Create(ctx context.Context, s *Session) error
    Get(ctx context.Context, id string) (*Session, error)
    GetByCheckoutID(ctx context.Context, checkoutID string) (*Session, error)
    Transition(ctx context.Context, id string, from, to State, u *Update) error
    ConsumeIfConfirmed(ctx context.Context, id string) (*Session, error)
    ExpireStale(ctx context.Context, before time.Time) (int64, error)
    ListPending(ctx context.Context, before time.Time) ([]*Session, error)
}
```

Redis, PostgreSQL, and other backends are community-implementable via this interface.

---

## Development
```bash
git clone https://github.com/brandon-kigen/malipo
cd malipo
go mod tidy
go build ./...
go test -race ./...
```

No Docker, no external services required. The unit test suite runs entirely against the in-memory adapter. Integration tests use an in-memory SQLite database.

For testing against real Safaricom APIs, copy `.env.example` to `.env` and fill in your Daraja sandbox credentials. Use a local tunnel such as `ngrok` to expose your callback URL.

---

## Project Structure
```
malipo/
├── auth/
│   ├── manager.go          Manager struct, GetAccessToken, GeneratePassword
│   └── daraja.go           fetchToken, SendSTKPush, QuerySTKStatus, Daraja HTTP
├── session/
│   ├── state.go            Event type, validTransitions, resultCodeToEvent, queryResultCodeToEvent
│   ├── manager.go          Manager, NewManager, InitiatePayment, HandleCallback, ConsumeIfConfirmed
│   ├── token.go            TokenProvider interface
│   ├── phone.go            E.164 phone normalisation
│   └── ttl.go              expireAfter goroutine, startCleanupTicker, runRecovery, Stop
├── store/
│   ├── adapter.go          StorageAdapter interface
│   ├── session.go          Session, Update, STKPushRequest structs
│   ├── state.go            State type, constants, IsTerminal, sentinel errors
│   ├── memory/
│   │   └── memory.go       In-memory adapter — unit tests
│   └── sqlite/
│       ├── sqlite.go       SQLite adapter — production default
│       ├── schema.sql      CREATE TABLE, WAL pragma, partial indexes
│       └── queries/        Embedded SQL — one file per query
├── x402/
│   ├── scheme.go           SchemeName, Network, PaymentHeader, wire types
│   ├── response.go         Response402, Write402
│   └── x402.go             GateOptions, buildRequirements, Gate middleware
├── callback/
│   └── callback.go         NewHandler, ServeHTTP, payload types, metadata extraction
└── _examples/
    ├── stdlib/             net/http standard library example _(Phase 6)_
    ├── chi/                Chi router integration example _(Phase 6)_
    ├── gin/                Gin integration example _(Phase 6)_
    └── echo/               Echo integration example _(Phase 6)_
```

---

## Roadmap

| Phase | Description | Status |
|---|---|---|
| 1 | Storage layer — interfaces, memory adapter, SQLite adapter | ✅ Complete |
| 2 | Auth Manager — token lifecycle, Daraja OAuth, STK Push | ✅ Complete |
| 3 | Session Manager — state machine, TTL, InitiatePayment | ✅ Complete |
| 4 | x402 Middleware — Gate, 402 responses, ConsumeIfConfirmed | ✅ Complete |
| 5 | Callback Handler — validation, lost callback recovery, AWAITING_PIN | ✅ Complete |
| 6 | Integration tests, examples, documentation | ⏳ Pending |

---

## Scope — BYOC Model

Malipo is a **Bring Your Own Credentials** SDK. It runs entirely within your infrastructure:

- No Malipo servers in the payment path
- No float management — uses your M-Pesa business shortcode directly
- No user data leaves your server except to Safaricom
- Apache 2.0 licensed

You are responsible for Daraja API credentials, M-Pesa compliance, and float management. Malipo handles the protocol bridging only.

---

## License

Apache 2.0
