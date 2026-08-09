package inbox

type Message struct {
	RecipientActorID string
	SenderActorID    string
	ActivityURI      string
	Type             string
	RawJSON          []byte
	SignatureValid   bool
}

type ProcessingState string

const (
	StatePending   ProcessingState = "pending"
	StateProcessed ProcessingState = "processed"
	StateFailed    ProcessingState = "failed"
	StateIgnored   ProcessingState = "ignored"
)
