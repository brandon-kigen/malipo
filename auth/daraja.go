package auth

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/brandon-kigen/malipo/store"
)

// stkTransactionType is the Daraja transaction type for STK Push.
// Always "CustomerPayBillOnline" — not configurable.
const stkTransactionType = "CustomerPayBillOnline"

// tokenResponse mirrors the JSON body returned by the Daraja OAuth endpoint.
// ExpiresIn is a string in the Daraja response — parsed to int64 by GetAccessToken.
type tokenResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   string `json:"expires_in"`
}

// stkPushBody mirrors the JSON body Daraja expects for an STK Push request.
type stkPushBody struct {
	BusinessShortCode string `json:"BusinessShortCode"`
	Password          string `json:"Password"`
	Timestamp         string `json:"Timestamp"`
	TransactionType   string `json:"TransactionType"`
	Amount            string `json:"Amount"`
	PartyA            string `json:"PartyA"`
	PartyB            string `json:"PartyB"`
	PhoneNumber       string `json:"PhoneNumber"`
	CallBackURL       string `json:"CallBackURL"`
	AccountReference  string `json:"AccountReference"`
	TransactionDesc   string `json:"TransactionDesc"`
}

// stkPushResponse mirrors the JSON body Daraja returns on a successful
// STK Push request.
type stkPushResponse struct {
	CheckoutRequestID   string `json:"CheckoutRequestID"`
	MerchantRequestID   string `json:"MerchantRequestID"`
	ResponseCode        string `json:"ResponseCode"`
	ResponseDescription string `json:"ResponseDescription"`
}

// fetchToken makes a GET request to the Daraja OAuth endpoint and returns
// a raw Bearer token and its expiry duration as strings.
//
// Parsing expires_in to an integer and caching are the caller's responsibility
// — this function is deliberately thin and returns exactly what Daraja sends.
//
// The Authorization header uses HTTP Basic auth with the consumerKey and
// consumerSecret base64-encoded as "consumerKey:consumerSecret".
func (m *Manager) fetchToken(ctx context.Context) (token, expiresIn string, err error) {
	// Build Basic auth header — base64(consumerKey:consumerSecret)
	credentials := base64.StdEncoding.EncodeToString(
		[]byte(m.cfg.ConsumerKey + ":" + m.cfg.ConsumerSecret),
	)

	url := m.cfg.Environment.baseURL() + "/oauth/v1/generate?grant_type=client_credentials"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", "", fmt.Errorf("fetchToken: failed to build request: %w", err)
	}

	req.Header.Set("Authorization", "Basic "+credentials)

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("fetchToken: request failed: %w", err)
	}
	// Deferred immediately after confirming resp is non-nil — ensures the
	// connection is released regardless of status code or decode outcome.
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("fetchToken: unexpected status %d", resp.StatusCode)
	}

	var result tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", "", fmt.Errorf("fetchToken: failed to decode response: %w", err)
	}

	return result.AccessToken, result.ExpiresIn, nil
}

// SendSTKPush makes a POST request to the Daraja STK Push endpoint,
// triggering a PIN prompt on the customer's SIM card.
//
// It returns the CheckoutRequestID and MerchantRequestID Daraja assigns
// to the request — these are stored on the session and used to correlate
// the eventual callback from Safaricom.
//
// The caller is responsible for providing a valid Bearer token via
// req.Token — obtained from GetAccessToken before calling this method.
func (m *Manager) SendSTKPush(ctx context.Context, req store.STKPushRequest) (checkoutID, merchantID string, err error) {
	// Build the Daraja request body.
	// Amount is converted from int64 to string — Daraja expects a string.
	// PartyA and PhoneNumber are both the customer phone number.
	// PartyB and BusinessShortCode are both the merchant shortcode.
	body := stkPushBody{
		BusinessShortCode: req.Shortcode,
		Password:          req.Password,
		Timestamp:         req.Timestamp,
		TransactionType:   stkTransactionType,
		Amount:            strconv.FormatInt(req.Amount, 10),
		PartyA:            req.Phone,
		PartyB:            req.Shortcode,
		PhoneNumber:       req.Phone,
		CallBackURL:       req.CallbackURL,
		AccountReference:  req.Reference,
		TransactionDesc:   req.Desc,
	}

	// Encode the body to JSON into a buffer.
	// json.NewEncoder writes directly into buf — no intermediate allocation.
	buf := new(bytes.Buffer)
	if err := json.NewEncoder(buf).Encode(body); err != nil {
		return "", "", fmt.Errorf("sendSTKPush: encode failed: %w", err)
	}

	url := m.cfg.Environment.baseURL() + "/mpesa/stkpush/v1/processrequest"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, buf)
	if err != nil {
		return "", "", fmt.Errorf("sendSTKPush: request build failed: %w", err)
	}

	// Bearer auth — token obtained from GetAccessToken.
	httpReq.Header.Set("Authorization", "Bearer "+req.Token)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := m.httpClient.Do(httpReq)
	if err != nil {
		return "", "", fmt.Errorf("sendSTKPush: request failed: %w", err)
	}
	// Deferred immediately after confirming resp is non-nil — ensures the
	// connection is released regardless of status code or decode outcome.
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("sendSTKPush: unexpected status %d", resp.StatusCode)
	}

	var result stkPushResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", "", fmt.Errorf("sendSTKPush: decode failed: %w", err)
	}

	return result.CheckoutRequestID, result.MerchantRequestID, nil
}