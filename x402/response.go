// Package x402 implements the x402 HTTP payment protocol for M-Pesa STK Push.
// It provides middleware that gates net/http handlers behind a real mobile money
// payment, following the x402 spec's scheme-based payment requirements model.
//
// The flow is:
//   - Client requests a gated resource with no proof of payment
//   - Middleware initiates an STK Push and returns 402 with payment requirements
//   - Client polls for session confirmation then retries with X-PAYMENT header
//   - Middleware verifies the session and serves the resource
package x402

import (
	"encoding/json"
	"log"
	"net/http"
)

// Response402 is the JSON body of an x402 Payment Required response.
// Accepts is a slice to remain spec-compliant — x402 allows servers to
// advertise multiple payment options. Malipo always populates exactly one.
type Response402 struct {
	Accepts []PaymentRequirements `json:"accepts"`
	Error   string                `json:"error"`
}

// Write402 constructs and writes a spec-compliant 402 Payment Required
// response to w. It serialises reqs into the x402 envelope, sets
// Content-Type before writing the status code, and logs any write error
// without returning it — by the time w.Write is called the status is
// already sent and the error is unrecoverable by the caller.
//
// Returns an error only if JSON serialisation fails, in which case a 500
// is written instead and no 402 body is sent.
func Write402(w http.ResponseWriter, reqs PaymentRequirements) error {
	data := Response402{
		Accepts: []PaymentRequirements{reqs},
		Error:   "Payment required",
	}

	serialized, err := json.Marshal(data)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return err
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusPaymentRequired)
	_, err = w.Write(serialized)
	if err != nil {
		log.Printf("response write error: %v", err)
	}
	return nil
}
