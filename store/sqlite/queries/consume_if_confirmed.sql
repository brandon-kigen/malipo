UPDATE sessions
SET
    state       = 'CONSUMED',
    consumed_at = ?
WHERE id    = ?
AND   state = 'CONFIRMED';