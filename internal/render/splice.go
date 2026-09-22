package render

import (
	"bytes"
	"errors"
	"fmt"
	"strings"

	"github.com/leoru/gitident-cli/internal/paths"
)

// ErrCorruptBlock is returned when the markers in a gitconfig are unbalanced.
var ErrCorruptBlock = errors.New("managed block markers are unbalanced")

// findBlock locates the managed block. It returns the byte offsets of the start of
// the begin-marker line and the end of the end-marker line (including its newline).
func findBlock(data []byte) (start, end int, found bool, err error) {
	start, end = -1, -1
	offset := 0
	for offset < len(data) {
		lineEnd := bytes.IndexByte(data[offset:], '\n')
		next := len(data)
		if lineEnd >= 0 {
			next = offset + lineEnd + 1
		}
		line := strings.TrimRight(string(data[offset:next]), "\r\n")
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, beginPrefix):
			if start >= 0 {
				return 0, 0, false, fmt.Errorf("%w: found a second begin marker before the end marker", ErrCorruptBlock)
			}
			if end >= 0 {
				return 0, 0, false, fmt.Errorf("%w: found more than one managed block", ErrCorruptBlock)
			}
			start = offset
		case strings.HasPrefix(trimmed, endPrefix):
			if start < 0 {
				return 0, 0, false, fmt.Errorf("%w: end marker without a begin marker", ErrCorruptBlock)
			}
			if end >= 0 {
				return 0, 0, false, fmt.Errorf("%w: found more than one managed block", ErrCorruptBlock)
			}
			end = next
		}
		offset = next
	}
	if start >= 0 && end < 0 {
		return 0, 0, false, fmt.Errorf("%w: begin marker without an end marker", ErrCorruptBlock)
	}
	return start, end, start >= 0, nil
}

// Splice replaces the managed block in existing with block (which must use LF line
// endings and include the markers), or appends it when absent. Everything outside
// the block is left byte-identical. CRLF files get a CRLF block.
func Splice(existing, block []byte) ([]byte, error) {
	start, end, found, err := findBlock(existing)
	if err != nil {
		return nil, err
	}
	if bytes.Contains(existing, []byte("\r\n")) {
		block = bytes.ReplaceAll(block, []byte("\n"), []byte("\r\n"))
	}
	var out bytes.Buffer
	if found {
		out.Write(existing[:start])
		out.Write(block)
		out.Write(existing[end:])
		return out.Bytes(), nil
	}
	out.Write(existing)
	if len(existing) > 0 {
		nl := []byte("\n")
		if bytes.Contains(existing, []byte("\r\n")) {
			nl = []byte("\r\n")
		}
		if !bytes.HasSuffix(existing, []byte("\n")) {
			out.Write(nl)
		}
		out.Write(nl)
	}
	out.Write(block)
	return out.Bytes(), nil
}

// ExtractBlock returns the current managed block (with LF endings) if present.
func ExtractBlock(data []byte) ([]byte, bool, error) {
	start, end, found, err := findBlock(data)
	if err != nil || !found {
		return nil, found, err
	}
	return bytes.ReplaceAll(data[start:end], []byte("\r\n"), []byte("\n")), true, nil
}

// RemoveBlock returns data with the managed block (and one preceding blank line
// that Splice added) removed.
func RemoveBlock(data []byte) ([]byte, bool, error) {
	start, end, found, err := findBlock(data)
	if err != nil || !found {
		return data, false, err
	}
	head := data[:start]
	switch {
	case bytes.HasSuffix(head, []byte("\r\n\r\n")):
		head = head[:len(head)-2]
	case bytes.HasSuffix(head, []byte("\n\n")):
		head = head[:len(head)-1]
	}
	out := append(append([]byte{}, head...), data[end:]...)
	return out, true, nil
}

// ForeignLines returns lines inside the managed block that gitident never
// writes — typically settings added by `git config --global`, which would be
// lost when the block is rewritten.
func ForeignLines(data []byte) ([]string, error) {
	blk, found, err := ExtractBlock(data)
	if err != nil || !found {
		return nil, err
	}
	var foreign []string
	for _, line := range strings.Split(string(blk), "\n") {
		t := strings.TrimSpace(line)
		switch {
		case t == "", strings.HasPrefix(t, "#"), strings.HasPrefix(t, ";"),
			strings.HasPrefix(t, "[includeIf "), t == "[include]",
			strings.HasPrefix(t, "path = "):
		default:
			foreign = append(foreign, t)
		}
	}
	return foreign, nil
}

// CorruptBlockHelp explains how to recover from ErrCorruptBlock.
func CorruptBlockHelp(file string) string {
	return fmt.Sprintf(`%s contains a damaged %s managed block.
Open it in an editor and make sure there is exactly one pair of lines:
  %s
  ...
  %s
(or delete both markers and everything between them), then run the command again.`,
		file, paths.AppName, BeginMarker, EndMarker)
}
