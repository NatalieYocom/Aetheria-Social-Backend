ALTER TABLE instance_join_tickets ADD COLUMN client_did TEXT;
-- Tickets issued before key binding must not survive this security migration.
UPDATE instance_join_tickets SET expires_at=LEAST(expires_at,now()) WHERE consumed_at IS NULL;
ALTER TABLE instance_join_tickets ADD CONSTRAINT join_ticket_client_did_length CHECK (client_did IS NULL OR length(client_did) BETWEEN 50 AND 64);
