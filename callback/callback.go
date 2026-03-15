package callback

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"

	"github.com/brandon-kigen/malipo/session"
	"github.com/brandon-kigen/malipo/store"
)

// callbackBody is the top-level envelope Safaricom POSTs to the CallbackURL.
type callbackBody struct {
	Body struct {
		STKCallback stkCallback `json:"stkCallback"`
	} `json:"Body"`
}

// stkCallback carries the result of an STK Push request.
// CallbackMetadata is nil when ResultCode is non-zero — payment was not successful.
type stkCallback struct {
	MerchantRequestID string            `json:"MerchantRequestID"`
	CheckoutRequestID string            `json:"CheckoutRequestID"`
	ResultCode        int               `json:"ResultCode"`
	ResultDesc        string            `json:"ResultDesc"`
	CallbackMetadata  *callbackMetadata `json:"CallbackMetadata"`
}

// callbackMetadata carries the transaction details on a successful callback.
// Only present when ResultCode is 0.
type callbackMetadata struct {
	Item []callbackItem `json:"Item"`
}

// callbackItem is a single key-value pair in the callback metadata.
// Value is interface{} — Safaricom sends numeric values as JSON numbers
// and string values as JSON strings. Type assertions are required on extraction.
type callbackItem struct {
	Name  string `json:"Name"`
	Value any    `json:"Value"`
}

// itemValue returns the Value for the given Name from a metadata item slice.
// Returns nil if the name is not found.
func itemValue(items []callbackItem, name string) any {
	for _, item := range items {
		if item.Name == name {
			return item.Value
		}
	}
	return nil
}

// HandlerConfig configures the Safaricom STK Push callback handler.
type HandlerConfig struct {
	Manager *session.Manager
}

type handler struct {
	manager *session.Manager
}

// NewHandler returns an http.Handler that processes Safaricom STK Push
// callbacks posted to the developer's CallbackURL.
//
// Mount this handler at the same path you passed as CallbackURL when
// constructing the session.Manager:
//
//	mux.Handle("/mpesa/callback", callback.NewHandler(callback.HandlerConfig{
//	    Manager: manager,
//	}))
func NewHandler(cfg HandlerConfig) http.Handler {
	if cfg.Manager == nil {
		panic("callback: HandlerConfig.Manager must not be nil")
	}
	return &handler{manager: cfg.Manager}
}

// ServeHTTP processes a single Safaricom STK Push callback.
// Always responds 200 — Safaricom does not retry and ignores the response body.
// Errors after the session lookup are logged and silently absorbed.
func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Step 1 — method guard
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	// Step 2 — decode
	var body callbackBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	cb := body.Body.STKCallback

	// Step 3 — checkout ID must be present to look anything up
	if cb.CheckoutRequestID == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	// Step 4 — build update from metadata when ResultCode is 0
	var u *store.Update
	if cb.ResultCode == 0 && cb.CallbackMetadata != nil {
		items := cb.CallbackMetadata.Item

		var amount int64
		if v := itemValue(items, "Amount"); v != nil {
			if f, ok := v.(float64); ok {
				amount = int64(f)
			}
		}

		var receipt string
		if v := itemValue(items, "MpesaReceiptNumber"); v != nil {
			if s, ok := v.(string); ok {
				receipt = s
			}
		}

		var phone string
		if v := itemValue(items, "PhoneNumber"); v != nil {
			if f, ok := v.(float64); ok {
				phone = fmt.Sprintf("+%.0f", f)
			}
		}

		u = &store.Update{
			MpesaReceiptNumber: &receipt,
			ConfirmedAmount:    &amount,
			ConfirmedPhone:     &phone,
		}
	}

	// Step 5 — fire the transition and always return 200
	if err := h.manager.HandleCallback(r.Context(), cb.CheckoutRequestID, cb.ResultCode, u); err != nil {
		log.Printf("callback: HandleCallback error for %s: %v", cb.CheckoutRequestID, err)
	}

	w.WriteHeader(http.StatusOK)
}
