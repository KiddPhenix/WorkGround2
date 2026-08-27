package assistant

import (
	"context"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// ReplyPreview is a best-effort, incremental preview of the Dispatcher's
// user-facing reply. The final validated Dispatch.Reply stays authoritative;
// a preview never replaces the persisted value.
type ReplyPreview struct {
	AssistantID string
	DispatchID  string
	RequestID   string
	Reply       string
}

// ReplyObserver receives incremental, escape-decoded previews of the JSON
// "reply" string while a streaming Dispatcher model runs. Implementations must
// treat these as provisional: they must never be persisted or presented as
// committed until the final Dispatch is validated.
type ReplyObserver func(ReplyPreview)

// StreamRoleModel is a RoleModel that can also stream its output deltas. The
// final text returned by CompleteStream is authoritative; deltas are only used
// for previews.
type StreamRoleModel interface {
	RoleModel
	CompleteStream(ctx context.Context, prompt string, onDelta func(string)) (string, error)
}

// replyStreamDecoder incrementally extracts and decodes the JSON string value of
// the "reply" field from a streaming JSON object. It handles JSON escapes
// (\", \\, \/, \b, \f, \n, \r, \t and \uXXXX including surrogate pairs) and
// only emits decoded, non-raw text. It is deliberately best-effort: malformed
// escape tails are dropped rather than shown as raw JSON.
type replyStreamDecoder struct {
	state    replyStreamState
	keyWin   []byte
	esc      bool
	unicode  bool
	hex      [4]byte
	hexN     int
	high     rune
	haveHigh bool
	acc      strings.Builder
	reply    string
	changed  bool
}

type replyStreamState int

const (
	replySeekKey replyStreamState = iota
	replySeekColon
	replySeekQuote
	replyInString
	replyDone
)

var replyKeyBytes = []byte(`"reply"`)

// Feed consumes the next chunk of raw model output and returns the cumulative
// decoded reply ("" until the reply key and opening quote are found) and whether
// this chunk changed that value.
func (d *replyStreamDecoder) Feed(chunk string) (string, bool) {
	d.changed = false
	for i := 0; i < len(chunk); i++ {
		d.feedByte(chunk[i])
	}
	return d.reply, d.changed
}

func (d *replyStreamDecoder) feedByte(b byte) {
	switch d.state {
	case replySeekKey:
		d.keyWin = append(d.keyWin, b)
		if len(d.keyWin) > len(replyKeyBytes) {
			d.keyWin = d.keyWin[1:]
		}
		if len(d.keyWin) == len(replyKeyBytes) && string(d.keyWin) == string(replyKeyBytes) {
			d.state = replySeekColon
		}
	case replySeekColon:
		if isJSONSpace(b) {
			return
		}
		if b == ':' {
			d.state = replySeekQuote
			return
		}
		d.resumeKey(b)
	case replySeekQuote:
		if isJSONSpace(b) {
			return
		}
		if b == '"' {
			d.state = replyInString
			return
		}
		d.resumeKey(b)
	case replyInString:
		d.consumeStringByte(b)
	case replyDone:
		// The reply value has been fully decoded; trailing JSON is irrelevant.
	}
}

func (d *replyStreamDecoder) resumeKey(b byte) {
	d.state = replySeekKey
	d.keyWin = append(d.keyWin[:0], b)
}

func (d *replyStreamDecoder) consumeStringByte(b byte) {
	if d.unicode {
		if isHexDigit(b) {
			d.hex[d.hexN] = b
			d.hexN++
			if d.hexN == 4 {
				d.emitUnicode()
				d.unicode = false
				d.esc = false
			}
			return
		}
		// Malformed \u escape: drop the pending sequence and treat b normally.
		d.unicode = false
		d.esc = false
	}

	if d.esc {
		switch b {
		case '"':
			d.appendByte('"')
		case '\\':
			d.appendByte('\\')
		case '/':
			d.appendByte('/')
		case 'b':
			d.appendByte('\b')
		case 'f':
			d.appendByte('\f')
		case 'n':
			d.appendByte('\n')
		case 'r':
			d.appendByte('\r')
		case 't':
			d.appendByte('\t')
		case 'u':
			d.unicode = true
			d.hexN = 0
			return
		default:
			// Unknown escape: emit the escaped byte verbatim.
			d.appendByte(b)
		}
		d.esc = false
		return
	}

	if b == '\\' {
		d.esc = true
		return
	}
	if b == '"' {
		d.reply = d.acc.String()
		d.state = replyDone
		return
	}
	d.appendByte(b)
}

func (d *replyStreamDecoder) emitUnicode() {
	unit := parseHex4(d.hex)
	if d.haveHigh {
		if unit >= 0xDC00 && unit <= 0xDFFF {
			d.acc.WriteRune(utf16.DecodeRune(d.high, unit))
			d.haveHigh = false
			d.high = 0
			d.changed = true
			return
		}
		// High surrogate not followed by a low surrogate.
		d.acc.WriteRune(utf8.RuneError)
		d.haveHigh = false
		d.high = 0
		d.changed = true
		// fall through to process the current unit
	}
	if unit >= 0xD800 && unit <= 0xDBFF {
		d.high = unit
		d.haveHigh = true
		return
	}
	if unit >= 0xDC00 && unit <= 0xDFFF {
		d.acc.WriteRune(utf8.RuneError)
		d.changed = true
		return
	}
	d.acc.WriteRune(rune(unit))
	d.changed = true
}

func (d *replyStreamDecoder) appendByte(b byte) {
	d.acc.WriteByte(b)
	d.changed = true
}

func isJSONSpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}

func isHexDigit(b byte) bool {
	return (b >= '0' && b <= '9') || (b >= 'a' && b <= 'f') || (b >= 'A' && b <= 'F')
}

func parseHex4(hex [4]byte) rune {
	var out rune
	for _, b := range hex {
		out <<= 4
		switch {
		case b >= '0' && b <= '9':
			out |= rune(b - '0')
		case b >= 'a' && b <= 'f':
			out |= rune(b-'a') + 10
		case b >= 'A' && b <= 'F':
			out |= rune(b-'A') + 10
		}
	}
	return out
}
