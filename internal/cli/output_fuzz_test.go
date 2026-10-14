package cli

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"google.golang.org/protobuf/types/known/timestamppb"

	sockv1 "github.com/guygrigsby/sock-contract/gen/go/sock/v1"
)

// terminalSafe fails t when out holds a control character (C0, DEL, C1)
// other than tab and newline, or invalid UTF-8: a sender must never be able
// to drive the terminal through what sock prints. newlines is how many
// newlines out may hold.
func terminalSafe(t *testing.T, what, out string, newlines int) {
	t.Helper()
	if !utf8.ValidString(out) {
		t.Fatalf("%s: output is not valid UTF-8: %q", what, out)
	}
	if i := strings.IndexFunc(out, func(r rune) bool { return unicode.IsControl(r) && r != '\n' && r != '\t' }); i >= 0 {
		t.Fatalf("%s: control character at %d reaches the terminal: %q", what, i, out)
	}
	if n := strings.Count(out, "\n"); n > newlines || !strings.HasSuffix(out, "\n") {
		t.Fatalf("%s: %d newlines (at most %d, one at the end): %q", what, n, newlines, out)
	}
}

func FuzzPrinterText(f *testing.F) {
	f.Add("m1", "c1", "acc-1", "hello", "Ada", "ada", "label")
	f.Add("m1", "c1", "acc-1", "\x1b]0;owned\x07\x1b[2Jclear", "\x1b[31mred", "h\x00", "l\r\n")
	f.Add("m\n2", "c\r1", "acc\x9b1", "multi\nline\ttext", "\u009b31m", "\x7f", "\xff")
	f.Add("", "", "", "", "", "", "")
	f.Fuzz(func(t *testing.T, id, conv, sender, text, name, handle, label string) {
		p := Printer{}
		ts := timestamppb.New(timestamppb.Now().AsTime())
		var b bytes.Buffer

		m := &sockv1.Message{Id: id, ConversationId: conv, SenderId: sender, Seq: 3, SentAt: ts, Body: &sockv1.Message_Text{Text: text}}
		if err := p.Message(&b, m); err != nil {
			t.Fatal(err)
		}
		terminalSafe(t, "Message", b.String(), strings.Count(text, "\n")+1)

		for _, a := range []*sockv1.Account{
			{Id: id, DisplayName: name, Kind: &sockv1.Account_Human{Human: &sockv1.Human{Handle: handle}}},
			{Id: id, DisplayName: name, Kind: &sockv1.Account_Puppet{Puppet: &sockv1.Puppet{Name: label, OwnerId: sender}}},
		} {
			b.Reset()
			if err := p.Account(&b, a); err != nil {
				t.Fatal(err)
			}
			terminalSafe(t, "Account", b.String(), 1)
		}

		b.Reset()
		if err := p.Conversation(&b, &sockv1.Conversation{Id: conv, MemberIds: []string{sender, id}}); err != nil {
			t.Fatal(err)
		}
		terminalSafe(t, "Conversation", b.String(), 1)

		b.Reset()
		if err := p.Device(&b, &sockv1.Device{Id: id, Label: label, CreatedAt: ts}, true, "  ", sockv1.DeviceKind_DEVICE_KIND_SESSION); err != nil {
			t.Fatal(err)
		}
		terminalSafe(t, "Device", b.String(), 1)

		b.Reset()
		if err := p.Blocked(&b, &sockv1.BlockedAccount{AccountId: sender, BlockedAt: ts}); err != nil {
			t.Fatal(err)
		}
		terminalSafe(t, "Blocked", b.String(), 1)

		// Printable text survives as is.
		if strings.IndexFunc(text, unicode.IsControl) < 0 && utf8.ValidString(text) {
			b.Reset()
			_ = p.Message(&b, m)
			if !strings.Contains(b.String(), ": "+text+"\n") {
				t.Fatalf("printable text %q altered: %q", text, b.String())
			}
		}
	})
}

// FuzzPrinterJSON checks --json output is one line of JSON with no raw C0
// control character, whatever the message holds.
func FuzzPrinterJSON(f *testing.F) {
	f.Add("m1", "hello")
	f.Add("m\n1", "\x1b[2J\r\n\u2028\u0085")
	f.Fuzz(func(t *testing.T, id, text string) {
		if !utf8.ValidString(id) || !utf8.ValidString(text) {
			return // protojson refuses invalid UTF-8 in strings
		}
		var b bytes.Buffer
		if err := (Printer{JSON: true}).Message(&b, &sockv1.Message{Id: id, Body: &sockv1.Message_Text{Text: text}}); err != nil {
			t.Fatal(err)
		}
		out := b.String()
		if strings.Count(out, "\n") != 1 || !strings.HasSuffix(out, "\n") {
			t.Fatalf("not one line: %q", out)
		}
		if i := strings.IndexFunc(strings.TrimSuffix(out, "\n"), func(r rune) bool { return r < 0x20 }); i >= 0 {
			t.Fatalf("raw C0 at %d: %q", i, out)
		}
	})
}

func FuzzTokenDeviceID(f *testing.F) {
	for _, s := range []string{"sock_st_dev1_secret", "sock_pt_dev2_x_y", "sock_mk_dev3_z", "sock_st_", "sock_st_nounderscore", "garbage", ""} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, token string) {
		got := tokenDeviceID(token)
		rest, ok := strings.CutPrefix(token, humanTokenPrefix)
		if !ok {
			rest, ok = strings.CutPrefix(token, puppetTokenPrefix)
		}
		if !ok {
			if got != "" {
				t.Fatalf("tokenDeviceID(%q) = %q for a token with no device", token, got)
			}
			return
		}
		if strings.Contains(got, "_") || !strings.HasPrefix(rest, got) {
			t.Fatalf("tokenDeviceID(%q) = %q", token, got)
		}
		if rest != got && !strings.HasPrefix(rest, got+"_") {
			t.Fatalf("tokenDeviceID(%q) = %q stops short of the separator", token, got)
		}
	})
}

func FuzzLabelOr(f *testing.F) {
	f.Add("", "sock CLI on laptop")
	f.Add("mine", "x")
	f.Add(strings.Repeat("\u00e9", 65), "x")
	f.Add("", strings.Repeat("h", 200))
	f.Add("", "\xff\xfe")
	f.Fuzz(func(t *testing.T, flag, dflt string) {
		got, err := labelOr(flag, dflt)
		if flag != "" {
			tooLong := utf8.RuneCountInString(flag) > maxLabel
			if tooLong != (err != nil) || (err == nil && got != flag) {
				t.Fatalf("labelOr(%q) = %q, %v", flag, got, err)
			}
			return
		}
		if err != nil {
			t.Fatalf("labelOr default: %v", err)
		}
		if utf8.RuneCountInString(got) > maxLabel || !strings.HasPrefix(string([]rune(dflt)), got) {
			t.Fatalf("labelOr(%q, %q) = %q", flag, dflt, got)
		}
	})
}

func FuzzReadText(f *testing.F) {
	f.Add("hello world", "", false, true)
	f.Add("", "piped\n", false, false)
	f.Add("", "", true, false)
	f.Add("", "\xff", false, false)
	f.Add("a", "", false, true)
	f.Fuzz(func(t *testing.T, arg, stdin string, isTTY, useArgs bool) {
		var args []string
		if useArgs && arg != "" {
			args = strings.Fields(arg)
		}
		got, err := readText(args, strings.NewReader(stdin), isTTY)
		if err != nil {
			return
		}
		if got == "" || len(got) > maxTextBytes || !utf8.ValidString(got) {
			t.Fatalf("readText accepted %q", got)
		}
		switch {
		case len(args) > 0:
			if got != strings.Join(args, " ") {
				t.Fatalf("readText(args %q) = %q", args, got)
			}
		case isTTY:
			t.Fatal("readText read a terminal")
		default:
			if got != strings.TrimSuffix(stdin, "\n") {
				t.Fatalf("readText(stdin %q) = %q", stdin, got)
			}
		}
	})
}

func FuzzReadEvidence(f *testing.F) {
	f.Add(`{"id":"m1","conversationId":"c1","seq":"2","senderId":"a","text":"hi","evidenceTag":"dGFn"}` + "\n")
	f.Add(`{"id":"m1","text":"no tag"}`)
	f.Add("\n\n   \n")
	f.Add(`{"id":"m1","evidenceTag":"dGFn","unknownField":1}`)
	f.Add(strings.Repeat(`{"id":"m","evidenceTag":"dA=="}`+"\n", 21))
	f.Add("not json")
	f.Fuzz(func(t *testing.T, data string) {
		path := t.TempDir() + "/evidence.jsonl"
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		msgs, err := readEvidence(path)
		if err != nil {
			return
		}
		if len(msgs) > maxEvidence {
			t.Fatalf("readEvidence returned %d messages", len(msgs))
		}
		lines := 0
		for l := range strings.Lines(data) {
			if strings.TrimSpace(l) != "" {
				lines++
			}
		}
		if len(msgs) != lines {
			t.Fatalf("readEvidence returned %d messages for %d non-blank lines", len(msgs), lines)
		}
		for _, m := range msgs {
			if len(m.GetEvidenceTag()) == 0 {
				t.Fatalf("message %q without an evidence tag", m.GetMessageId())
			}
		}
	})
}
