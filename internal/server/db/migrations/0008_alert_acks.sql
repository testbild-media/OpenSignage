-- Device problems a user acknowledged (one row per device and alert kind). The row is
-- deleted when the server sees the problem gone, so the problem shows again when it comes
-- back.
CREATE TABLE alert_acks (
  device_id  TEXT NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
  kind       TEXT NOT NULL,
  acked_at   TEXT NOT NULL,
  user_id    TEXT,                     -- no FK: the acknowledgement survives user deletion
  username   TEXT NOT NULL DEFAULT '', -- name at the time of the acknowledgement
  PRIMARY KEY (device_id, kind)
);
