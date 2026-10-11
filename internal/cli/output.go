package cli

import (
	"fmt"
	"io"
	"strings"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	sockv1 "github.com/guygrigsby/sock-contract/gen/go/sock/v1"
)

// Printer writes messages, accounts and conversations as text or JSON lines.
type Printer struct{ JSON bool }

func (p Printer) json(w io.Writer, m proto.Message) error {
	b, err := protojson.Marshal(m)
	if err != nil {
		return fmt.Errorf("encode output: %w", err)
	}
	_, err = fmt.Fprintf(w, "%s\n", b)
	return err
}

// Message prints one message: local time, conversation, seq, sender, text.
func (p Printer) Message(w io.Writer, m *sockv1.Message) error {
	if p.JSON {
		return p.json(w, m)
	}
	_, err := fmt.Fprintf(w, "%s  %s #%d  %s: %s\n",
		m.GetSentAt().AsTime().Local().Format(time.DateTime),
		m.GetConversationId(), m.GetSeq(), m.GetSenderId(), m.GetText())
	return err
}

// Account prints who an account is.
func (p Printer) Account(w io.Writer, a *sockv1.Account) error {
	if p.JSON {
		return p.json(w, a)
	}
	var kind string
	switch k := a.GetKind().(type) {
	case *sockv1.Account_Human:
		kind = "human @" + k.Human.GetHandle()
	case *sockv1.Account_Puppet:
		kind = "puppet " + k.Puppet.GetName() + " of " + k.Puppet.GetOwnerId()
	default:
		kind = "unknown kind"
	}
	_, err := fmt.Fprintf(w, "%s  %s  id %s\n", a.GetDisplayName(), kind, a.GetId())
	return err
}

// Conversation prints a conversation and its members.
func (p Printer) Conversation(w io.Writer, c *sockv1.Conversation) error {
	if p.JSON {
		return p.json(w, c)
	}
	kind := "group"
	if c.GetKind() == sockv1.ConversationKind_CONVERSATION_KIND_DIRECT {
		kind = "direct"
	}
	_, err := fmt.Fprintf(w, "%s  %s  members %s\n", c.GetId(), kind, strings.Join(c.GetMemberIds(), ","))
	return err
}
