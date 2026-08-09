package resolver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

type RemoteActor struct {
	ActorURI          string
	Acct              string
	Type              string
	PreferredUsername string
	Name              string
	Domain            string
	InboxURL          string
	OutboxURL         string
	FollowersURL      string
	FollowingURL      string
	SharedInbox       string
	PublicKeyPEM      string
	RawJSON           []byte
}

type Resolver interface {
	ResolveActor(ctx context.Context, actorURI string) (RemoteActor, error)
}

type HTTPResolver struct {
	client       *http.Client
	maxBodyBytes int64
	lookup       LookupIPFunc
}

type LookupIPFunc func(context.Context, string) ([]net.IP, error)

func NewHTTPResolver(client *http.Client, maxBodyBytes int64) HTTPResolver {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second, Transport: otelhttp.NewTransport(http.DefaultTransport)}
	}
	if maxBodyBytes <= 0 {
		maxBodyBytes = 5 * 1024 * 1024
	}
	return HTTPResolver{
		client:       client,
		maxBodyBytes: maxBodyBytes,
		lookup:       defaultLookupIP,
	}
}

func (r HTTPResolver) ResolveActor(ctx context.Context, actorURI string) (RemoteActor, error) {
	safe, err := IsSafeRemoteURLWithLookup(ctx, actorURI, r.lookupFunc())
	if err != nil {
		return RemoteActor{}, err
	}
	if !safe {
		return RemoteActor{}, fmt.Errorf("blocked remote actor URI: %s", actorURI)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, actorURI, nil)
	if err != nil {
		return RemoteActor{}, err
	}
	req.Header.Set("Accept", `application/activity+json, application/ld+json; profile="https://www.w3.org/ns/activitystreams", application/json`)

	res, err := r.client.Do(req)
	if err != nil {
		return RemoteActor{}, err
	}
	defer res.Body.Close()

	if res.StatusCode < 200 || res.StatusCode > 299 {
		return RemoteActor{}, fmt.Errorf("remote actor fetch returned status %d", res.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(res.Body, r.maxBodyBytes+1))
	if err != nil {
		return RemoteActor{}, err
	}
	if int64(len(body)) > r.maxBodyBytes {
		return RemoteActor{}, errors.New("remote actor response exceeded maximum size")
	}

	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		return RemoteActor{}, err
	}

	parsed, _ := url.Parse(actorURI)
	actor := RemoteActor{
		ActorURI:          stringValue(doc["id"]),
		Type:              stringValue(doc["type"]),
		PreferredUsername: stringValue(doc["preferredUsername"]),
		Name:              stringValue(doc["name"]),
		Domain:            parsed.Hostname(),
		InboxURL:          stringValue(doc["inbox"]),
		OutboxURL:         stringValue(doc["outbox"]),
		FollowersURL:      stringValue(doc["followers"]),
		FollowingURL:      stringValue(doc["following"]),
		RawJSON:           body,
	}
	if actor.ActorURI == "" {
		actor.ActorURI = actorURI
	}
	if actor.Type == "" {
		actor.Type = "Person"
	}
	if actor.PreferredUsername == "" {
		actor.PreferredUsername = usernameFromActorURI(actor.ActorURI)
	}
	if actor.Name == "" {
		actor.Name = actor.PreferredUsername
	}
	if actor.Acct == "" && actor.PreferredUsername != "" && actor.Domain != "" {
		actor.Acct = actor.PreferredUsername + "@" + actor.Domain
	}
	if publicKey, ok := doc["publicKey"].(map[string]any); ok {
		actor.PublicKeyPEM = stringValue(publicKey["publicKeyPem"])
	}
	if endpoints, ok := doc["endpoints"].(map[string]any); ok {
		actor.SharedInbox = stringValue(endpoints["sharedInbox"])
	}
	if err := r.validateActorEndpointURLs(ctx, actor); err != nil {
		return RemoteActor{}, err
	}
	return actor, nil
}

func (r HTTPResolver) validateActorEndpointURLs(ctx context.Context, actor RemoteActor) error {
	endpoints := map[string]string{
		"inbox":       actor.InboxURL,
		"outbox":      actor.OutboxURL,
		"followers":   actor.FollowersURL,
		"following":   actor.FollowingURL,
		"sharedInbox": actor.SharedInbox,
	}
	for name, rawURL := range endpoints {
		if strings.TrimSpace(rawURL) == "" {
			continue
		}
		safe, err := IsSafeRemoteURLWithLookup(ctx, rawURL, r.lookupFunc())
		if err != nil {
			return fmt.Errorf("validate remote actor %s URL: %w", name, err)
		}
		if !safe {
			return fmt.Errorf("blocked remote actor %s URL: %s", name, rawURL)
		}
	}
	return nil
}

func (r HTTPResolver) lookupFunc() LookupIPFunc {
	if r.lookup != nil {
		return r.lookup
	}
	return defaultLookupIP
}

func IsHTTPActorURI(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return parsed.Scheme == "http" || parsed.Scheme == "https"
}

func IsSafeRemoteURL(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil {
		return false
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "" || host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return false
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return true
	}
	return !isPrivateOrLocalIP(ip)
}

func IsSafeRemoteURLWithLookup(ctx context.Context, raw string, lookup LookupIPFunc) (bool, error) {
	if !IsSafeRemoteURL(raw) {
		return false, nil
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return false, nil
	}
	host := strings.ToLower(parsed.Hostname())
	ip := net.ParseIP(host)
	if ip != nil {
		return !isPrivateOrLocalIP(ip), nil
	}
	if lookup == nil {
		lookup = defaultLookupIP
	}
	ips, err := lookup(ctx, host)
	if err != nil {
		return false, err
	}
	if len(ips) == 0 {
		return false, nil
	}
	for _, ip := range ips {
		if ip == nil || isPrivateOrLocalIP(ip) {
			return false, nil
		}
	}
	return true, nil
}

func defaultLookupIP(ctx context.Context, host string) ([]net.IP, error) {
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	ips := make([]net.IP, 0, len(addrs))
	for _, addr := range addrs {
		ips = append(ips, addr.IP)
	}
	return ips, nil
}

func isPrivateOrLocalIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsPrivate() {
		return true
	}
	return false
}

func stringValue(value any) string {
	stringValue, _ := value.(string)
	return stringValue
}

func usernameFromActorURI(actorURI string) string {
	parsed, err := url.Parse(actorURI)
	if err != nil {
		return "remote"
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) == 0 || parts[len(parts)-1] == "" {
		return "remote"
	}
	return parts[len(parts)-1]
}
