-- Phase 1 Gate-1 forward correction: platform-cap lifecycle alignment (037b).
--
-- This migration runs AFTER 037a (which owns the deferred coherence, state-row
-- protection, managed-snapshot guard, and the original revision-immutability
-- trigger). It is additive, idempotent, and FORWARD-ONLY. The paired .down.sql
-- FAILS CLOSED and must never be applied; downgrades are handled by backup
-- restore or a later forward corrective migration.
--
-- SCOPE (tightly bounded): replace ONLY the revision-immutability trigger/function
-- `trg_platform_cap_revision_immutable()` introduced by 037a. Nothing else from
-- 037, 037a, enrollment, roles, VM/server, frontend, node, deployment, or
-- workflow is touched.
--
-- DEFECT CORRECTED: 037a permitted only candidate->candidate/active and REJECTED
-- candidate->retired. But QuotaPolicyRepository.RetireCapRevision intentionally
-- supports retiring a stale candidate (it sets state='retired', retired_at=now,
-- leaving activated_at=NULL) — so 037a broke the repository contract. 037b makes
-- the lifecycle contract match the repository behavior while preserving audit:
--
--   * revision DELETE remains forbidden;
--   * immutable payload columns: id, max_vms, max_vcpu, max_ram_mb, max_disk_gb,
--     revision, created_by, note, created_at;
--   * legal transitions:
--       candidate -> candidate   (activated_at NULL, retired_at NULL)
--       candidate -> active      (activated_at NOT NULL, retired_at NULL)
--       candidate -> retired     (activated_at NULL, retired_at NOT NULL)  -- withdrawn before activation
--       active    -> active      (activated_at UNCHANGED, retired_at NULL)
--       active    -> retired     (activated_at UNCHANGED, retired_at NOT NULL)
--       retired   -> retired     (both timestamps IMMUTABLE)
--   * no resurrection (retired is terminal);
--   * no direct timestamp mutation within a stable state;
--   * INSERTs obey lifecycle timestamps:
--       candidate: both NULL
--       active:    activated_at NOT NULL, retired_at NULL
--       retired:   activated_at NULL, retired_at NOT NULL
--
-- This stays compatible with ActivateCapRevision (retire old active + activate
-- candidate + move pointer) and RetireCapRevision (active withdrawal AND
-- stale-candidate retirement), validated by 037a's deferred coherence checks.

-- Replace ONLY the revision-immutability function. CREATE OR REPLACE lets the
-- new body supersede 037a's while keeping the trigger name and table intact.
CREATE OR REPLACE FUNCTION trg_platform_cap_revision_immutable()
RETURNS trigger AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        -- Revisions are immutable snapshots: deletion is rejected; retire instead.
        RAISE EXCEPTION
            'platform_cap_revision_immutable: quota-cap revisions are immutable and must not be deleted (retire instead)'
            USING errcode = 'P0001';
    END IF;

    -- INSERT: validate lifecycle timestamps for the initial state.
    IF TG_OP = 'INSERT' THEN
        IF NEW.state = 'candidate' THEN
            IF NEW.activated_at IS NOT NULL OR NEW.retired_at IS NOT NULL THEN
                RAISE EXCEPTION 'platform_cap_revision_lifecycle: candidate must have no activated_at/retired_at'
                    USING errcode = 'P0001';
            END IF;
        ELSIF NEW.state = 'active' THEN
            IF NEW.activated_at IS NULL OR NEW.retired_at IS NOT NULL THEN
                RAISE EXCEPTION 'platform_cap_revision_lifecycle: active must have activated_at and no retired_at'
                    USING errcode = 'P0001';
            END IF;
        ELSIF NEW.state = 'retired' THEN
            -- Withdrawn-before-activation may be inserted directly as retired with
            -- no activation timestamp.
            IF NEW.activated_at IS NOT NULL OR NEW.retired_at IS NULL THEN
                RAISE EXCEPTION 'platform_cap_revision_lifecycle: retired must have retired_at and no activated_at'
                    USING errcode = 'P0001';
            END IF;
        END IF;
    END IF;

    -- UPDATE: validate the transition (old -> new) and timestamp stability.
    IF TG_OP = 'UPDATE' THEN
        -- Legal state transitions.
        IF OLD.state = 'candidate' THEN
            IF NEW.state NOT IN ('candidate', 'active', 'retired') THEN
                RAISE EXCEPTION 'platform_cap_revision_illegal_transition: candidate may stay candidate, become active, or be retired (withdrawn before activation)'
                    USING errcode = 'P0001';
            END IF;
        ELSIF OLD.state = 'active' THEN
            IF NEW.state NOT IN ('active', 'retired') THEN
                RAISE EXCEPTION 'platform_cap_revision_illegal_transition: active may stay active or become retired'
                    USING errcode = 'P0001';
            END IF;
        ELSIF OLD.state = 'retired' THEN
            IF NEW.state <> 'retired' THEN
                RAISE EXCEPTION 'platform_cap_revision_illegal_transition: retired is terminal (no resurrection)'
                    USING errcode = 'P0001';
            END IF;
        END IF;

        -- Lifecycle timestamp contract for the transition.
        IF NEW.state = 'candidate' THEN
            -- candidate (whether staying candidate or arriving from nowhere) must
            -- carry no lifecycle timestamps; this also blocks direct timestamp
            -- mutation within the candidate stable state.
            IF NEW.activated_at IS NOT NULL OR NEW.retired_at IS NOT NULL THEN
                RAISE EXCEPTION 'platform_cap_revision_lifecycle: candidate must have no activated_at/retired_at'
                    USING errcode = 'P0001';
            END IF;
        ELSIF NEW.state = 'active' THEN
            IF NEW.retired_at IS NOT NULL THEN
                RAISE EXCEPTION 'platform_cap_revision_lifecycle: active must have no retired_at'
                    USING errcode = 'P0001';
            END IF;
            IF NEW.activated_at IS NULL THEN
                RAISE EXCEPTION 'platform_cap_revision_lifecycle: active must have activated_at'
                    USING errcode = 'P0001';
            END IF;
            -- activated_at is immutable once set: a candidate (OLD NULL) is
            -- allowed to acquire its activation timestamp, but an already-active
            -- revision (OLD NOT NULL) may not have it rewritten, and a stale
            -- candidate may not have one injected without activating.
            IF OLD.activated_at IS NOT NULL AND OLD.activated_at IS DISTINCT FROM NEW.activated_at THEN
                RAISE EXCEPTION 'platform_cap_revision_lifecycle: activated_at is immutable once set'
                    USING errcode = 'P0001';
            END IF;
        ELSIF NEW.state = 'retired' THEN
            IF NEW.retired_at IS NULL THEN
                RAISE EXCEPTION 'platform_cap_revision_lifecycle: retired must have retired_at'
                    USING errcode = 'P0001';
            END IF;
            -- activated_at must remain unchanged: for active->retired it stays as
            -- it was; for candidate->retired (withdrawn before activation) it must
            -- remain NULL.
            IF OLD.activated_at IS DISTINCT FROM NEW.activated_at THEN
                RAISE EXCEPTION 'platform_cap_revision_lifecycle: activated_at is immutable'
                    USING errcode = 'P0001';
            END IF;
            -- Staying retired: retired_at is also immutable (both timestamps frozen).
            IF OLD.state = 'retired' AND OLD.retired_at IS DISTINCT FROM NEW.retired_at THEN
                RAISE EXCEPTION 'platform_cap_revision_lifecycle: retired_at is immutable once retired'
                    USING errcode = 'P0001';
            END IF;
        END IF;

        -- Immutable payload columns must not change (note is part of the
        -- immutable snapshot and is rejected from mutation).
        IF OLD.id <> NEW.id
           OR OLD.max_vms <> NEW.max_vms
           OR OLD.max_vcpu <> NEW.max_vcpu
           OR OLD.max_ram_mb <> NEW.max_ram_mb
           OR OLD.max_disk_gb <> NEW.max_disk_gb
           OR OLD.revision <> NEW.revision
           OR OLD.created_by IS DISTINCT FROM NEW.created_by
           OR OLD.created_at IS DISTINCT FROM NEW.created_at
           OR OLD.note IS DISTINCT FROM NEW.note THEN
            RAISE EXCEPTION 'platform_cap_revision_immutable: id/limits/revision/created_*/note are immutable'
                USING errcode = 'P0001';
        END IF;
    END IF;

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- Recreate the trigger exactly as 037a defined it (same name, same timing).
DROP TRIGGER IF EXISTS trg_platform_cap_revision_immutable ON platform_quota_cap_revisions;
CREATE TRIGGER trg_platform_cap_revision_immutable
    BEFORE INSERT OR UPDATE OR DELETE ON platform_quota_cap_revisions
    FOR EACH ROW EXECUTE FUNCTION trg_platform_cap_revision_immutable();
