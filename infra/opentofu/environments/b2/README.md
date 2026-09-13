# Backblaze B2 for off-site backups

`tofu init && tofu apply` here creates the bucket and the write-only key
described in `main.tf`. It needs `B2_APPLICATION_KEY_ID` and
`B2_APPLICATION_KEY` in the environment: an application key created in the
Backblaze console with the capabilities `listBuckets, writeBuckets,
deleteBuckets, listKeys, writeKeys, deleteKeys, listFiles, readFiles`
(the console's "All" access is fine for a hobbynet), stored in the bundle as
`b2_admin_key_id` / `b2_admin_key` and exported by `scripts/secrets env`.
State is local (`terraform.tfstate`, git-ignored) like the other
environments. Full sequence: `docs/runbooks/enable-offsite-backups.md`.
