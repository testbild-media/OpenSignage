-- Pairing requests an operator hid in the device list (the screen keeps showing its PIN;
-- the request reappears when the screen asks again after a restart or expiry).
ALTER TABLE pairing_requests ADD COLUMN dismissed_at TEXT;
