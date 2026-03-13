UPDATE sessions
SET state = 'TIMEOUT'
WHERE state NOT IN ('CONSUMED', 'TIMEOUT', 'CANCELLED', 'FAILED')
AND   expires_at < ?;