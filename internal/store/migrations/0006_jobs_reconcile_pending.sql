-- SPDX-License-Identifier: AGPL-3.0-only
-- An interrupted restore, revert, delete or recreate stays pending until reconciliation at
-- startup settles it from its journal: it is tried again at every start and the job purge keeps
-- it (docs 11 §8). Jobs interrupted before this version are not marked: their start already
-- reconciled them, and a journal run again against what changed since could undo later work.
ALTER TABLE jobs ADD COLUMN reconcile_pending INTEGER NOT NULL DEFAULT 0;
CREATE INDEX jobs_reconcile_pending ON jobs(reconcile_pending) WHERE reconcile_pending = 1;
