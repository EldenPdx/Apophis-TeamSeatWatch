-- +goose Up
-- Profiles are assigned only at an explicit account-session reservation. Existing
-- encrypted sessions/history and persisted Owner runtime settings remain intact.
CREATE TABLE tsw_account_browser_environments (
    profile_id uuid PRIMARY KEY,
    mother_account_id uuid UNIQUE REFERENCES tsw_mother_accounts(id) ON DELETE CASCADE,
    target_account_id uuid UNIQUE REFERENCES tsw_target_accounts(id) ON DELETE CASCADE,
    seed bigint NOT NULL UNIQUE CHECK (seed > 0),
    device_id uuid NOT NULL UNIQUE,
    recipe_version text NOT NULL,
    recipe jsonb NOT NULL CHECK (jsonb_typeof(recipe)='object'),
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK (num_nonnulls(mother_account_id,target_account_id)=1)
);
ALTER TABLE tsw_proxy_pool_settings ALTER COLUMN task_concurrency SET DEFAULT 5;

-- +goose Down
ALTER TABLE tsw_proxy_pool_settings ALTER COLUMN task_concurrency SET DEFAULT 3;
DROP TABLE tsw_account_browser_environments;
