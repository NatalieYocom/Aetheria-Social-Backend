package webfinger

type Resource struct {
	Subject string `json:"subject"`
	Links   []Link `json:"links"`
}

type Link struct {
	Rel  string `json:"rel"`
	Type string `json:"type,omitempty"`
	Href string `json:"href"`
}

func BuildActorResource(acct string, actorURI string) Resource {
	return Resource{
		Subject: "acct:" + acct,
		Links: []Link{
			{
				Rel:  "self",
				Type: "application/activity+json",
				Href: actorURI,
			},
		},
	}
}
