package x402

const (
	SchemeName    string = "mpesa"
	Network       string = "safaricom-ke"
	PaymentHeader string = "X-PAYMENT"
)

type PaymentRequirements struct {
	Scheme      string `json:"scheme"`
	Network     string `json:"network"`
	Amount      int64  `json:"amount"`
	Currency    string `json:"currency"`
	Resource    string `json:"resource"`
	Description string `json:"description"`
	PayTo       string `json:"payTo"`
	SessionID   string `json:"sessionId"`
	RetryAfter  int    `json:"retryAfter"`
}

type PaymentProof struct {
	SessionID string `json:"sessionId"`
}
