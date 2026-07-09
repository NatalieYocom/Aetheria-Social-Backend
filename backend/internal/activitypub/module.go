package activitypub

import "net/http"

// Module is intentionally small in MVP. It keeps federation-specific code
// isolated so WebFinger, inbox/outbox, delivery and resolver logic can be added
// without coupling Basis clients to ActivityPub internals.
type Module struct {
	Enabled bool
}

func NewModule(enabled bool) Module {
	return Module{Enabled: enabled}
}

func (m Module) RegisterPlaceholderRoutes(mux interface {
	Handle(pattern string, handler http.Handler)
}) {
	_ = mux
}
