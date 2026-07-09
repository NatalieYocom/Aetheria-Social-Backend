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
