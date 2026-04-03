package nats

import (
	"fmt"

	natsgo "github.com/nats-io/nats.go"
)

// NewJetStream connects to NATS and returns a JetStream context and the underlying connection.
// The caller is responsible for closing the connection when done.
func NewJetStream(natsURL string) (natsgo.JetStreamContext, *natsgo.Conn, error) {
	nc, err := natsgo.Connect(natsURL)
	if err != nil {
		return nil, nil, fmt.Errorf("connecting to NATS: %w", err)
	}

	js, err := nc.JetStream()
	if err != nil {
		nc.Close()
		return nil, nil, fmt.Errorf("creating JetStream context: %w", err)
	}

	return js, nc, nil
}
