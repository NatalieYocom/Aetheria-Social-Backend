package inbox

import "errors"

var ErrNotImplemented = errors.New("activitypub inbox processing is not implemented in this MVP")

type Message struct {
	RecipientActorID string
	SenderActorID    string
	ActivityURI      string
	Type             string
	RawJSON          []byte
	SignatureValid   bool
}

type Processor interface {
	Process(message Message) error
}

type PlaceholderProcessor struct{}

func (PlaceholderProcessor) Process(Message) error {
	return ErrNotImplemented
}
