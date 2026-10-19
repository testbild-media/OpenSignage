-- A time window can be limited to a period of dates ("happy hour on the 7th only"): the
-- first and last day (YYYY-MM-DD in the server time zone, inclusive) on which an occurrence of
-- the window may start; NULL = open.
ALTER TABLE schedule_rules ADD COLUMN date_from TEXT;
ALTER TABLE schedule_rules ADD COLUMN date_until TEXT;
