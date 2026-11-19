package broker

import (
	"testing"

	"github.com/nats-io/nats.go/jetstream"

	"rule-router/internal/rule"
)

// TestNewActionMsg_MsgID pins how an action's MsgID becomes the Nats-Msg-Id
// header the output stream deduplicates on.
func TestNewActionMsg_MsgID(t *testing.T) {
	tests := []struct {
		name    string
		action  rule.NATSAction
		want    string
		headers int
	}{
		{
			name:    "no MsgID adds no header",
			action:  rule.NATSAction{Subject: "out", Payload: "{}"},
			want:    "",
			headers: 0,
		},
		{
			name:    "MsgID becomes the header",
			action:  rule.NATSAction{Subject: "out", Payload: "{}", MsgID: "IN:42:abc"},
			want:    "IN:42:abc",
			headers: 1,
		},
		{
			name:    "MsgID kept alongside rule headers",
			action:  rule.NATSAction{Subject: "out", Payload: "{}", Headers: map[string]string{"X-Source": "s"}, MsgID: "IN:42:abc"},
			want:    "IN:42:abc",
			headers: 2,
		},
		{
			// NATS header names are case-sensitive and the server dedupes on
			// this exact spelling only, so that is the one that counts.
			name:    "rule's own Nats-Msg-Id wins",
			action:  rule.NATSAction{Subject: "out", Payload: "{}", Headers: map[string]string{"Nats-Msg-Id": "mine"}, MsgID: "IN:42:abc"},
			want:    "mine",
			headers: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := newActionMsg(&tt.action)
			if got := msg.Header.Get(jetstream.MsgIDHeader); got != tt.want {
				t.Errorf("Nats-Msg-Id = %q, want %q", got, tt.want)
			}
			if len(msg.Header) != tt.headers {
				t.Errorf("header count = %d, want %d (%v)", len(msg.Header), tt.headers, msg.Header)
			}
		})
	}
}
