UPDATE sessions
SET
    state               = ?,
    checkout_request_id = COALESCE(?, checkout_request_id),
    merchant_request_id = COALESCE(?, merchant_request_id),
    mpesa_receipt_number = COALESCE(?, mpesa_receipt_number),
    confirmed_amount    = COALESCE(?, confirmed_amount),
    confirmed_phone     = COALESCE(?, confirmed_phone),
    consumed_at         = COALESCE(?, consumed_at)
WHERE id    = ?
AND   state = ?;