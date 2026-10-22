-- SPDX-License-Identifier: AGPL-3.0-only
-- The audit entry of the action that queued a job (action, target type and target id, as JSON)
-- is written in the same transaction as the job; when the job ends its result is written under
-- the same action (docs 08 §9). Empty for jobs queued without one, and for those of earlier
-- versions.
ALTER TABLE jobs ADD COLUMN audit TEXT NOT NULL DEFAULT '';
