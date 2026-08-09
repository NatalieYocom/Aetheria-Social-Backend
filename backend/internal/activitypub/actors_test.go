package activitypub_test

import (
	"testing"

	"basisvr-social-service/internal/activitypub"
)

func TestBuildLocalActorMetadataUsesCanonicalURLs(t *testing.T) {
	meta := activitypub.BuildLocalActorMetadata(activitypub.LocalActorInput{
		PublicURL:           "https://basis.social/",
		Username:            "vadim",
		DisplayName:         "Vadim",
		Bio:                 "BasisVR world creator",
		AvatarURL:           "https://cdn.basis.social/avatars/vadim.png",
		PublicKeyPEM:        "-----BEGIN PUBLIC KEY-----\nkey\n-----END PUBLIC KEY-----",
		PrivateKeyEncrypted: "encrypted",
	})

	if meta.ActorURI != "https://basis.social/users/vadim" {
		t.Fatalf("ActorURI = %q", meta.ActorURI)
	}
	if meta.Acct != "vadim@basis.social" {
		t.Fatalf("Acct = %q", meta.Acct)
	}
	if meta.InboxURL != "https://basis.social/users/vadim/inbox" {
		t.Fatalf("InboxURL = %q", meta.InboxURL)
	}
	if meta.OutboxURL != "https://basis.social/users/vadim/outbox" {
		t.Fatalf("OutboxURL = %q", meta.OutboxURL)
	}
	if meta.FollowersURL != "https://basis.social/users/vadim/followers" {
		t.Fatalf("FollowersURL = %q", meta.FollowersURL)
	}
	if meta.FollowingURL != "https://basis.social/users/vadim/following" {
		t.Fatalf("FollowingURL = %q", meta.FollowingURL)
	}
	if !meta.IsLocal {
		t.Fatal("local actor metadata should be marked local")
	}
}

func TestBuildPersonActorDocumentKeepsActivityPubShape(t *testing.T) {
	meta := activitypub.ActorMetadata{
		ActorURI:          "https://basis.social/users/vadim",
		PreferredUsername: "vadim",
		DisplayName:       "Vadim",
		Summary:           "BasisVR world creator",
		InboxURL:          "https://basis.social/users/vadim/inbox",
		OutboxURL:         "https://basis.social/users/vadim/outbox",
		FollowersURL:      "https://basis.social/users/vadim/followers",
		FollowingURL:      "https://basis.social/users/vadim/following",
		AvatarURL:         "https://cdn.basis.social/avatars/vadim.png",
		PublicKeyPEM:      "public-key",
	}

	doc := activitypub.BuildPersonActorDocument(meta)

	if doc["id"] != meta.ActorURI {
		t.Fatalf("id = %v", doc["id"])
	}
	if doc["type"] != "Person" {
		t.Fatalf("type = %v", doc["type"])
	}
	if doc["preferredUsername"] != "vadim" {
		t.Fatalf("preferredUsername = %v", doc["preferredUsername"])
	}
	if doc["inbox"] != meta.InboxURL || doc["outbox"] != meta.OutboxURL {
		t.Fatalf("unexpected inbox/outbox in document: %+v", doc)
	}

	publicKey, ok := doc["publicKey"].(map[string]any)
	if !ok {
		t.Fatalf("publicKey should be an object: %#v", doc["publicKey"])
	}
	if publicKey["id"] != "https://basis.social/users/vadim#main-key" {
		t.Fatalf("publicKey.id = %v", publicKey["id"])
	}
}

func TestBuildLocalGroupActorMetadata(t *testing.T) {
	meta := activitypub.BuildLocalGroupActorMetadata(activitypub.LocalActorInput{
		PublicURL: "https://basis.social/", Username: "world-creators", DisplayName: "World Creators",
	})
	if meta.Type != "Group" || meta.ActorURI != "https://basis.social/groups/world-creators" {
		t.Fatalf("meta = %+v", meta)
	}
	if meta.InboxURL != meta.ActorURI+"/inbox" || meta.FollowersURL != meta.ActorURI+"/followers" {
		t.Fatalf("group collection URLs = %+v", meta)
	}
	doc := activitypub.BuildGroupActorDocument(meta)
	if doc["type"] != "Group" || doc["id"] != meta.ActorURI {
		t.Fatalf("document = %+v", doc)
	}
}

func TestBuildServiceActorDocument(t *testing.T) {
	doc := activitypub.BuildServiceActorDocument(activitypub.ActorMetadata{
		ActorURI: "https://basis.social/actor", PreferredUsername: "instance",
		DisplayName: "BasisVR Social", PublicKeyPEM: "public-key",
		InboxURL: "https://basis.social/actor/inbox", OutboxURL: "https://basis.social/actor/outbox",
	})
	if doc["type"] != "Service" || doc["id"] != "https://basis.social/actor" {
		t.Fatalf("document = %+v", doc)
	}
	publicKey := doc["publicKey"].(map[string]any)
	if publicKey["owner"] != doc["id"] {
		t.Fatalf("publicKey = %+v", publicKey)
	}
}
