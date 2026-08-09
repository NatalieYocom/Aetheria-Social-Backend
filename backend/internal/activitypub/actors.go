package activitypub

import (
	"net/url"
	"strings"
)

type LocalActorInput struct {
	PublicURL           string
	Username            string
	DisplayName         string
	Bio                 string
	AvatarURL           string
	PublicKeyPEM        string
	PrivateKeyEncrypted string
}

type ActorMetadata struct {
	ActorURI            string
	Acct                string
	Type                string
	PreferredUsername   string
	DisplayName         string
	Summary             string
	Domain              string
	InboxURL            string
	OutboxURL           string
	FollowersURL        string
	FollowingURL        string
	SharedInboxURL      string
	PublicKeyPEM        string
	PrivateKeyEncrypted string
	IsLocal             bool
	AvatarURL           string
}

func BuildLocalActorMetadata(input LocalActorInput) ActorMetadata {
	return buildLocalActorMetadata(input, "users", "Person")
}

func BuildLocalGroupActorMetadata(input LocalActorInput) ActorMetadata {
	return buildLocalActorMetadata(input, "groups", "Group")
}

func buildLocalActorMetadata(input LocalActorInput, path, actorType string) ActorMetadata {
	base := strings.TrimRight(input.PublicURL, "/")
	username := strings.TrimSpace(input.Username)
	actorURI := base + "/" + path + "/" + url.PathEscape(username)
	domain := hostFromURL(base)

	return ActorMetadata{
		ActorURI:            actorURI,
		Acct:                username + "@" + domain,
		Type:                actorType,
		PreferredUsername:   username,
		DisplayName:         input.DisplayName,
		Summary:             input.Bio,
		Domain:              domain,
		InboxURL:            actorURI + "/inbox",
		OutboxURL:           actorURI + "/outbox",
		FollowersURL:        actorURI + "/followers",
		FollowingURL:        actorURI + "/following",
		SharedInboxURL:      base + "/inbox",
		PublicKeyPEM:        input.PublicKeyPEM,
		PrivateKeyEncrypted: input.PrivateKeyEncrypted,
		IsLocal:             true,
		AvatarURL:           input.AvatarURL,
	}
}

func BuildPersonActorDocument(meta ActorMetadata) map[string]any {
	meta.Type = "Person"
	return buildActorDocument(meta)
}

func BuildGroupActorDocument(meta ActorMetadata) map[string]any {
	meta.Type = "Group"
	return buildActorDocument(meta)
}

func BuildServiceActorDocument(meta ActorMetadata) map[string]any {
	meta.Type = "Service"
	return buildActorDocument(meta)
}

func buildActorDocument(meta ActorMetadata) map[string]any {
	doc := map[string]any{
		"@context": []any{
			"https://www.w3.org/ns/activitystreams",
			"https://w3id.org/security/v1",
			map[string]any{"basis": "https://basis.social/ns#"},
		},
		"id":                meta.ActorURI,
		"type":              meta.Type,
		"preferredUsername": meta.PreferredUsername,
		"name":              meta.DisplayName,
		"summary":           meta.Summary,
		"url":               publicProfileURL(meta),
		"inbox":             meta.InboxURL,
		"outbox":            meta.OutboxURL,
		"followers":         meta.FollowersURL,
		"following":         meta.FollowingURL,
		"publicKey": map[string]any{
			"id":           meta.ActorURI + "#main-key",
			"owner":        meta.ActorURI,
			"publicKeyPem": meta.PublicKeyPEM,
		},
	}
	if meta.AvatarURL != "" {
		doc["icon"] = map[string]any{
			"type":      "Image",
			"mediaType": "image/png",
			"url":       meta.AvatarURL,
		}
	}
	if meta.SharedInboxURL != "" {
		doc["endpoints"] = map[string]any{
			"sharedInbox": meta.SharedInboxURL,
		}
	}
	return doc
}

func publicProfileURL(meta ActorMetadata) string {
	if meta.ActorURI == "" || meta.PreferredUsername == "" {
		return ""
	}
	if meta.Type == "Group" || meta.Type == "Service" {
		return meta.ActorURI
	}
	base := strings.TrimSuffix(meta.ActorURI, "/users/"+url.PathEscape(meta.PreferredUsername))
	if base == meta.ActorURI {
		return ""
	}
	return base + "/@" + url.PathEscape(meta.PreferredUsername)
}

func hostFromURL(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Hostname() == "" {
		return "localhost"
	}
	return parsed.Hostname()
}
