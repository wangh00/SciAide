package events

import (
	"encoding/json"
	"time"
)

const CurrentVersion = 1

type Envelope struct {
	Version       int             `json:"version"`
	EventID       string          `json:"eventId"`
	AggregateID   string          `json:"aggregateId"`
	AggregateType string          `json:"aggregateType"`
	Sequence      int64           `json:"sequence"`
	Type          string          `json:"type"`
	Timestamp     time.Time       `json:"timestamp"`
	Payload       json.RawMessage `json:"payload"`
}

func New(eventID, aggregateID, aggregateType, eventType string, sequence int64, payload json.RawMessage) Envelope {
	return Envelope{
		Version:       CurrentVersion,
		EventID:       eventID,
		AggregateID:   aggregateID,
		AggregateType: aggregateType,
		Sequence:      sequence,
		Type:          eventType,
		Timestamp:     time.Now().UTC(),
		Payload:       payload,
	}
}
