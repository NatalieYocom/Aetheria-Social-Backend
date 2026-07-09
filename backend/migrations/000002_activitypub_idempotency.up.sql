CREATE UNIQUE INDEX IF NOT EXISTS inbox_messages_activity_uri_unique_idx
ON inbox_messages(activity_uri);

CREATE UNIQUE INDEX IF NOT EXISTS outbox_jobs_activity_target_unique_idx
ON outbox_jobs(activity_id, target_inbox_url);

