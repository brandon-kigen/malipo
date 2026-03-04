# Malipo

Go middleware SDK that bridges the [x402 HTTP payment protocol](https://x402.org) to the M-Pesa Daraja STK Push API. Drop it into any `net/http`-compatible server to gate resources behind real M-Pesa payments.

```
Client → GET /api/data
Server → 402 Payment Required
Client → POST /malipo/session (phone + amount)
         [Safaricom delivers PIN prompt to user's SIM]
Client → GET /malipo/session/{id} (polls until confirmed)
Client → GET /api/data + X-PAYMENT-SIGNATURE
Server → 200 OK + data
```

---

## Status

**Phase 1 — Storage layer (in progress)**

The `StorageAdapter` interface, `Session` and `State` types, and sentinel errors are defined. Memory and SQLite adapters are next.

---

## Design

Malipo is four packages with a strict one-way dependency chain:

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

The Session Manager never imports a concrete storage or auth type. Both are injected as interfaces at construction time. This keeps every package independently testable and keeps the dependency graph acyclic.

### The async gap problem

x402 assumes synchronous payment — client pays, server verifies, resource released in one cycle. M-Pesa STK Push is asynchronous — Safaricom delivers a PIN prompt to the user's SIM over its own network, the user decides, Safaricom posts a callback 5 to 30 seconds later.

Malipo solves this with a session state machine persisted to storage:

```
CREATED → STK_PUSHED → AWAITING_PIN → CONFIRMED → CONSUMED
                  ↓           ↓
               TIMEOUT    CANCELLED
                           FAILED
```

The session survives the async gap in storage. The x402 layer only releases the resource when `ConsumeIfConfirmed` transitions the session from `CONFIRMED` to `CONSUMED` atomically — one SQL statement is the entire double-spend prevention.

---

## Storage Backends

SQLite is the default and requires no configuration. For multi-instance deployments, provide a Redis adapter:

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
}
```

---

## Development Setup

```bash
git clone https://github.com/brandon-kigen/malipo
cd malipo
go mod tidy
go build ./...
go test ./...
```

No Docker, no external services required for tests. The test suite runs entirely against the in-memory adapter.

For testing against real Safaricom APIs, copy `.env.example` to `.env` and fill in your Daraja sandbox credentials. Use a local tunnel (e.g. `ngrok`) to expose your callback URL.

---

## Project Structure

```
malipo/
├── malipo.go           public API — New(), Config, Client
├── auth/               Daraja OAuth token management
├── session/            state machine, payment orchestration
├── store/              StorageAdapter interface + Session types
│   ├── memory/         in-memory adapter (tests)
│   └── sqlite/         SQLite adapter (default)
├── x402/               HTTP middleware — 402 responses, signature verification
├── callback/           Safaricom callback handler + validation pipeline
├── tools/gendocs/      dev tool — generates state machine diagrams
└── examples/
    ├── chi/            Chi router integration example
    └── stdlib/         net/http standard library example
```

---

## Scope — BYOC Model

Malipo is a **Bring Your Own Credentials** SDK. It runs entirely within your infrastructure:

- No Malipo servers in the payment path
- No float management — uses your M-Pesa business shortcode directly
- No user data leaves your server except to Safaricom
- Apache 2.0 licensed

You are responsible for Daraja API credentials, M-Pesa compliance, and float management. Malipo handles the protocol bridging only.

---

## Roadmap

| Phase | Description | Status |
|-------|-------------|--------|
| 1 | Storage layer — interfaces, memory adapter, SQLite adapter | In progress |
| 2 | Auth Manager — token lifecycle, Daraja OAuth | Pending |
| 3 | Session Manager — state machine, TTL, InitiatePayment | Pending |
| 4 | x402 Middleware — 402 responses, signature verification | Pending |
| 5 | Callback Handler — validation pipeline, lost callback recovery | Pending |
| 6 | Integration tests, examples, documentation | Pending |

---

## License

To Be Obtained - Apache 2.0