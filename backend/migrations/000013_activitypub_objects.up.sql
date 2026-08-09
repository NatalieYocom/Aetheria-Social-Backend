CREATE TABLE activitypub_objects (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  object_uri TEXT NOT NULL UNIQUE,
  attributed_to_actor_id UUID NOT NULL REFERENCES actors(id) ON DELETE CASCADE,
  type TEXT NOT NULL,
  raw_json JSONB NOT NULL,
  is_deleted BOOLEAN NOT NULL DEFAULT false,
  published_at TIMESTAMPTZ NULL,
  source_updated_at TIMESTAMPTZ NULL,
  received_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TRIGGER activitypub_objects_set_updated_at
BEFORE UPDATE ON activitypub_objects
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE INDEX activitypub_objects_actor_created_idx
  ON activitypub_objects(attributed_to_actor_id, created_at DESC, id DESC);
CREATE INDEX activitypub_objects_type_created_idx
  ON activitypub_objects(type, created_at DESC, id DESC)
  WHERE is_deleted = false;
