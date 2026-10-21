-- SPDX-License-Identifier: AGPL-3.0-only
-- The backups catalog grows with every backup and deleted rows stay until housekeeping prunes
-- them after a year: the queries that filter by status (running backups at startup, the
-- catalog check, the prune itself) use this index instead of reading the whole table.
CREATE INDEX backups_status ON backups(status, started_at);
