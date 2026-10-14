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
		localTime(m.GetSentAt()),
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

// localTime formats a server timestamp in the local zone.
func localTime(ts interface{ AsTime() time.Time }) string {
	return ts.AsTime().Local().Format(time.DateTime)
}

// deviceKinds names each device kind as the text output prints it.
var deviceKinds = map[sockv1.DeviceKind]string{
	sockv1.DeviceKind_DEVICE_KIND_SESSION:      "session",
	sockv1.DeviceKind_DEVICE_KIND_PUPPET_TOKEN: "puppet-token",
	sockv1.DeviceKind_DEVICE_KIND_MACHINE_KEY:  "machine-key",
}

// Device prints one device; this marks the calling device. indent prefixes
// the text line (puppet devices under their puppet). unset is the kind of a
// device whose kind is UNSPECIFIED, from a server before ADR 10: a session
// for a human's device, a puppet token for a puppet's.
func (p Printer) Device(w io.Writer, d *sockv1.Device, this bool, indent string, unset sockv1.DeviceKind) error {
	if p.JSON {
		return p.json(w, d)
	}
	mark := ""
	if this {
		mark = "  (this)"
	}
	kind := d.GetKind()
	if kind == sockv1.DeviceKind_DEVICE_KIND_UNSPECIFIED {
		kind = unset
	}
	_, err := fmt.Fprintf(w, "%s%s  %s  %s  created %s%s\n", indent, d.GetId(), deviceKinds[kind], d.GetLabel(), localTime(d.GetCreatedAt()), mark)
	return err
}

// Blocked prints one blocked account.
func (p Printer) Blocked(w io.Writer, b *sockv1.BlockedAccount) error {
	if p.JSON {
		return p.json(w, b)
	}
	_, err := fmt.Fprintf(w, "%s  blocked %s\n", b.GetAccountId(), localTime(b.GetBlockedAt()))
	return err
}

// Done prints a command's confirmation: the response message with --json,
// else text.
func (p Printer) Done(w io.Writer, res proto.Message, text string) error {
	if p.JSON {
		return p.json(w, res)
	}
	_, err := fmt.Fprintln(w, text)
	return err
}
