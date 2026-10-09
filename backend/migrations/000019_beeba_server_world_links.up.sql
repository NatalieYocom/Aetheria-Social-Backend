CREATE TABLE beeba_server_world_links (
 issuer TEXT NOT NULL,
 beeba_server_id UUID NOT NULL,
 beeba_user_id UUID NOT NULL,
 world_id UUID NOT NULL REFERENCES worlds(id) ON DELETE CASCADE,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 PRIMARY KEY(issuer,beeba_server_id),
 FOREIGN KEY(issuer,beeba_user_id) REFERENCES beeba_identity_links(issuer,beeba_user_id) ON DELETE CASCADE
);
CREATE INDEX beeba_server_world_links_world_idx ON beeba_server_world_links(world_id);
CREATE INDEX beeba_server_world_links_owner_idx ON beeba_server_world_links(issuer,beeba_user_id);
