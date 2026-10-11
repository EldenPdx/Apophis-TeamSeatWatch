-- +goose Up
-- Keep classified connection evidence beside the exact exchange attempt.
-- Existing failed attempts have no recoverable cause and remain NULL.
ALTER TABLE tsw_selected_workspace_tokens ADD COLUMN failure JSONB;
ALTER TABLE tsw_mother_personal_access ADD COLUMN failure JSONB;
ALTER TABLE tsw_mother_discoveries ADD COLUMN failure JSONB;
ALTER TABLE tsw_selected_workspace_tokens ADD CONSTRAINT tsw_workspace_token_failure_ck
    CHECK (failure IS NULL OR (status = 'failed' AND jsonb_typeof(failure) = 'object'));
ALTER TABLE tsw_mother_personal_access DROP CONSTRAINT tsw_mother_personal_access_status_check;
ALTER TABLE tsw_mother_personal_access ADD CONSTRAINT tsw_mother_personal_access_status_check
    CHECK (status IN ('verifying','ready','session_expired','invalid_login','missing_credentials','refresh_failed','unavailable'));

-- +goose Down
UPDATE tsw_mother_personal_access SET status='refresh_failed' WHERE status='session_expired';
ALTER TABLE tsw_mother_personal_access DROP CONSTRAINT tsw_mother_personal_access_status_check;
ALTER TABLE tsw_mother_personal_access ADD CONSTRAINT tsw_mother_personal_access_status_check
    CHECK (status IN ('verifying','ready','invalid_login','missing_credentials','refresh_failed','unavailable'));
ALTER TABLE tsw_selected_workspace_tokens DROP CONSTRAINT tsw_workspace_token_failure_ck;
ALTER TABLE tsw_selected_workspace_tokens DROP COLUMN failure;
ALTER TABLE tsw_mother_personal_access DROP COLUMN failure;
ALTER TABLE tsw_mother_discoveries DROP COLUMN failure;
