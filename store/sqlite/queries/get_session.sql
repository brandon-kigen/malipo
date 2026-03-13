SELECT
    id,
    state,
    phone,
    amount,
    currency,
    shortcode,
    checkout_request_id,
    merchant_request_id,
    mpesa_receipt_number,
    confirmed_amount,
    confirmed_phone,
    created_at,
    expires_at,
    consumed_at
FROM sessions
WHERE id = ?;