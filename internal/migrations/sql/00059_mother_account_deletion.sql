-- +goose Up
-- Removal hides the management record and fences new account actions while
-- retaining its credentials, workspace relationships and delivery history.
ALTER TABLE tsw_mother_accounts
 ADD COLUMN deleted_at timestamptz,
 ADD COLUMN deleted_by uuid REFERENCES tsw_owners(id) ON DELETE RESTRICT,
 ADD CONSTRAINT tsw_mother_account_deletion_actor CHECK ((deleted_at IS NULL) = (deleted_by IS NULL)),
 ADD CONSTRAINT tsw_mother_account_deleted_disabled CHECK (deleted_at IS NULL OR status='disabled');
CREATE INDEX tsw_mother_accounts_visible_idx
 ON tsw_mother_accounts(created_at DESC,id DESC) WHERE deleted_at IS NULL;

-- +goose Down
DROP INDEX tsw_mother_accounts_visible_idx;
ALTER TABLE tsw_mother_accounts
 DROP CONSTRAINT tsw_mother_account_deleted_disabled,
 DROP CONSTRAINT tsw_mother_account_deletion_actor,
 DROP COLUMN deleted_by,
 DROP COLUMN deleted_at;
