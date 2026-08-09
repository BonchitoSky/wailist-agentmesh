-- Per-user account settings, edited from the /settings page.
--
-- One row per user, created lazily on first write: GetUserSettings returns
-- defaults for a user with no row rather than 404ing, so signup does not have
-- to remember to seed this table and existing accounts keep working untouched.
--
-- The CHECK constraints are the safety property. They make an out-of-range
-- value a database error rather than something every future call site has to
-- remember to validate — the same approach 000017's tendril_credit_non_negative
-- takes for the Tendril balance.
CREATE TABLE IF NOT EXISTS user_settings (
    user_id                    TEXT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    -- Balance below which the UI warns. Default matches the frontend's
    -- previous hardcoded $5 (lib/credits/store.ts, app/billing/page.tsx).
    low_balance_usd_micros     BIGINT NOT NULL DEFAULT 5000000,
    -- Optional per-call spend ceiling, enforced in engine.Runner.preflightCheck.
    -- NULL means "no user ceiling" — the global MaxSingleX402QuoteUSDMicros
    -- still applies, so NULL is a weaker limit, never an unlimited one.
    max_call_spend_usd_micros  BIGINT,
    -- Which API key newly created Provider nodes default to. A node's own
    -- keyMode still wins; this only seeds the canvas.
    default_key_mode           TEXT   NOT NULL DEFAULT 'byok',
    updated_at                 TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT user_settings_key_mode_valid
        CHECK (default_key_mode IN ('byok', 'platform')),
    CONSTRAINT user_settings_low_balance_non_negative
        CHECK (low_balance_usd_micros >= 0),
    CONSTRAINT user_settings_max_call_spend_positive
        CHECK (max_call_spend_usd_micros IS NULL OR max_call_spend_usd_micros > 0)
);
