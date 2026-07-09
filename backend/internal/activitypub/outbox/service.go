package outbox

import "errors"

var ErrNotImplemented = errors.New("activitypub outbox publishing is not implemented in this MVP")

type Activity struct {
	ActivityURI string
	ActorID     string
	Type        string
	ObjectURI   string
	RawJSON     []byte
}

type Publisher interface {
	Publish(activity Activity) error
}

type PlaceholderPublisher struct{}

func (PlaceholderPublisher) Publish(Activity) error {
	return ErrNotImplemented
}
