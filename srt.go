package astisub

import (
	"bytes"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/net/html"
)

// Constants
const (
	srtTimeBoundariesSeparator = "-->"
)

// Vars
var (
	bytesSRTTimeBoundariesSeparator = []byte(" " + srtTimeBoundariesSeparator + " ")
)

// parseDurationSRT parses an .srt duration
func parseDurationSRT(i string) (d time.Duration, err error) {
	for _, s := range []string{",", ".", ":"} {
		if d, err = parseDuration(i, s, 3); err == nil {
			return
		}
	}
	return
}

// isSRTTimeBoundaries reports whether the line holds time boundaries: an arrow next to
// what can only be a duration, well-formed or not. Subtitles ripped from ass files may
// show arrows in their text, such as "#1 ---> #2", which hold no time boundaries.
func isSRTTimeBoundaries(line string) bool {
	idx := strings.Index(line, srtTimeBoundariesSeparator)
	if idx < 0 {
		return false
	}
	end := strings.Fields(line[idx+len(srtTimeBoundariesSeparator):])
	return isSRTDurationLike(line[:idx]) || (len(end) > 0 && isSRTDurationLike(end[0]))
}

// isSRTDurationLike reports whether the text is made of what an .srt duration is made of
func isSRTDurationLike(i string) bool {
	i = strings.TrimSpace(i)
	return strings.Contains(i, ":") && strings.Trim(i, "0123456789:,.- ") == ""
}

// ReadFromSRT parses an .srt content
func ReadFromSRT(i io.Reader) (o *Subtitles, err error) {
	// Read the content at once since whether braces hide comments depends on all of it
	var b bytes.Buffer
	if _, err = b.ReadFrom(i); err != nil {
		err = fmt.Errorf("astisub: reading srt failed: %w", err)
		return
	}
	ssaRip := isSSARip(b.String())

	// Init
	o = NewSubtitles()
	var scanner = newScanner(&b)

	// Scan
	var line string
	var lineNum int
	var s = &Item{}
	var sa = &StyleAttributes{}
	for scanner.Scan() {
		// Fetch line
		line = strings.TrimSpace(scanner.Text())
		lineNum++
		if !utf8.ValidString(line) {
			err = fmt.Errorf("astisub: line %d is not valid utf-8", lineNum)
			return
		}

		// Remove BOM header
		if lineNum == 1 {
			line = strings.TrimPrefix(line, string(BytesBOM))
		}

		// Line contains time boundaries
		if isSRTTimeBoundaries(line) {
			// Reset style attributes
			sa = &StyleAttributes{}

			// Remove last item of previous subtitle since it should be the index.
			// If the last line is empty then the item is missing an index.
			var index string
			if len(s.Lines) != 0 {
				index = s.Lines[len(s.Lines)-1].String()
				if index != "" {
					s.Lines = s.Lines[:len(s.Lines)-1]
				}
			}

			dropEmptyLines(s)

			// Init subtitle
			s = &Item{}

			// Fetch Index
			if index != "" {
				s.Index, _ = strconv.Atoi(index)
			}

			// Extract time boundaries
			s1 := strings.Split(line, srtTimeBoundariesSeparator)
			if l := len(s1); l < 2 {
				err = fmt.Errorf("astisub: line %d: time boundaries has only %d element(s)", lineNum, l)
				return
			}
			// We do this to eliminate extra stuff like positions which are not documented anywhere
			s2 := strings.Fields(s1[1])
			if len(s2) == 0 {
				err = fmt.Errorf("astisub: line %d: missing srt end time boundary", lineNum)
				return
			}

			// Parse time boundaries
			if s.StartAt, err = parseDurationSRT(s1[0]); err != nil {
				err = fmt.Errorf("astisub: line %d: parsing srt duration %s failed: %w", lineNum, s1[0], err)
				return
			}
			if s.EndAt, err = parseDurationSRT(s2[0]); err != nil {
				err = fmt.Errorf("astisub: line %d: parsing srt duration %s failed: %w", lineNum, s2[0], err)
				return
			}

			// Append subtitle
			o.Items = append(o.Items, s)
		} else {
			// Ass never displays braces: in a rip, those holding no override tag hide
			// comments
			if ssaRip {
				if t := ssaRegexpComment.ReplaceAllString(line, ""); t != line {
					if line = strings.TrimSpace(t); line == "" {
						continue
					}
				}
			}

			// Add text. A line ripped from an ass subtitle may hold several lines
			// of it, each of which is read as an srt line.
			for _, part := range splitSSALineBreaks(line) {
				if l := parseTextSrt(part, sa); len(l.Items) > 0 {
					s.Lines = append(s.Lines, l)
				}
			}

			// Alignment applies to the whole subtitle and not only to the text
			// following it, and only its first occurrence is taken into account
			if sa.SRTPosition != 0 && s.InlineStyle == nil {
				s.InlineStyle = &StyleAttributes{SRTPosition: sa.SRTPosition}
				s.InlineStyle.propagateSRTAttributes()
			}
		}
	}
	return
}

// dropEmptyLines removes the blank lines read into a subtitle: they
// separate subtitles and carry no text. Every text line is kept, wherever
// a blank line fell.
func dropEmptyLines(s *Item) {
	var lines []Line
	for _, l := range s.Lines {
		for len(l.Items) > 0 && len(l.Items[len(l.Items)-1].Text) == 0 {
			l.Items = l.Items[:len(l.Items)-1]
		}
		if len(l.Items) > 0 {
			lines = append(lines, l)
		}
	}
	s.Lines = lines
}

// parseTextSrt parses the input line to fill the Line
func parseTextSrt(i string, sa *StyleAttributes) (o Line) {
	// special handling needed for empty line
	if strings.TrimSpace(i) == "" {
		o.Items = []LineItem{{Text: ""}}
		return
	}

	// Create tokenizer
	tr := html.NewTokenizer(strings.NewReader(i))

	// Loop
	for {
		// Get next tag
		t := tr.Next()

		// Process error
		if err := tr.Err(); err != nil {
			break
		}

		// Get unmodified text
		raw := string(tr.Raw())
		// Get current token
		token := tr.Token()

		switch t {
		case html.EndTagToken:
			// Parse italic/bold/underline
			switch token.Data {
			case "b":
				sa.SRTBold = false
			case "i":
				sa.SRTItalics = false
			case "u":
				sa.SRTUnderline = false
			case "font":
				sa.SRTColor = nil
			}
		case html.StartTagToken:
			// Parse italic/bold/underline
			switch token.Data {
			case "b":
				sa.SRTBold = true
			case "i":
				sa.SRTItalics = true
			case "u":
				sa.SRTUnderline = true
			case "font":
				if c := htmlTokenAttribute(&token, "color"); c != nil {
					// Parse the color string into a Color struct
					sa.SRTColor = newColorFromHTMLString(*c)
				}
			}
		case html.TextToken:
			// Override tags such as {\an8} are not part of the srt format but files
			// ripped from ass subtitles keep them, in which case they must not be
			// displayed as text. Splitting on them makes sure they only style what
			// follows them.
			for _, part := range splitSSAOverrides(raw) {
				if part.tags != nil {
					applySRTOverrideTags(sa, part.tags)
					continue
				}

				// The text of an ass drawing is the commands tracing its shapes, which
				// a rip may also have kept without the tag starting the drawing
				if sa.ssaDrawing || isSSADrawing(part.text) {
					continue
				}

				if s := strings.TrimSpace(part.text); s != "" {
					// Get style attribute
					var styleAttributes *StyleAttributes
					if sa.SRTBold || sa.SRTColor != nil || sa.SRTItalics || sa.SRTUnderline {
						styleAttributes = &StyleAttributes{
							SRTBold:      sa.SRTBold,
							SRTColor:     sa.SRTColor,
							SRTItalics:   sa.SRTItalics,
							SRTUnderline: sa.SRTUnderline,
						}
						styleAttributes.propagateSRTAttributes()
					}

					// Append item
					o.Items = append(o.Items, LineItem{
						InlineStyle: styleAttributes,
						Text:        unescapeHTML(part.text),
					})
				}
			}
		}
	}
	return
}

// formatDurationSRT formats an .srt duration
func formatDurationSRT(i time.Duration) string {
	return formatDuration(i, ",", 3)
}

// WriteToSRT writes subtitles in .srt format
func (s Subtitles) WriteToSRT(o io.Writer) (err error) {
	// Do not write anything if no subtitles
	if len(s.Items) == 0 {
		err = ErrNoSubtitlesToWrite
		return
	}

	// Add BOM header
	var c []byte
	c = append(c, BytesBOM...)

	// Loop through subtitles
	for k, v := range s.Items {
		// Add time boundaries
		c = append(c, []byte(strconv.Itoa(k+1))...)
		c = append(c, bytesLineSeparator...)
		c = append(c, []byte(formatDurationSRT(v.StartAt))...)
		c = append(c, bytesSRTTimeBoundariesSeparator...)
		c = append(c, []byte(formatDurationSRT(v.EndAt))...)
		c = append(c, bytesLineSeparator...)

		// Add alignment, as an ass override tag since srt has none of its own
		if v.InlineStyle != nil && v.InlineStyle.SRTPosition != 0 {
			c = append(c, []byte(fmt.Sprintf(`{\an%d}`, v.InlineStyle.SRTPosition))...)
		}

		// Loop through lines
		for _, l := range v.Lines {
			c = append(c, []byte(l.srtBytes())...)
		}

		// Add new line
		c = append(c, bytesLineSeparator...)
	}

	// Remove last new line
	c = c[:len(c)-1]

	// Write
	if _, err = o.Write(c); err != nil {
		err = fmt.Errorf("astisub: writing failed: %w", err)
		return
	}
	return
}

func (l Line) srtBytes() (c []byte) {
	for _, li := range l.Items {
		c = append(c, li.srtBytes()...)
	}
	c = append(c, bytesLineSeparator...)
	return
}

func (li LineItem) srtBytes() (c []byte) {
	// Get color
	var color string
	if li.InlineStyle != nil && li.InlineStyle.SRTColor != nil {
		color = li.InlineStyle.SRTColor.HTMLString()
	}

	// Get bold/italics/underline
	b := li.InlineStyle != nil && li.InlineStyle.SRTBold
	i := li.InlineStyle != nil && li.InlineStyle.SRTItalics
	u := li.InlineStyle != nil && li.InlineStyle.SRTUnderline

	// Get position
	var pos byte
	if li.InlineStyle != nil {
		pos = li.InlineStyle.SRTPosition
	}

	// Append
	if color != "" {
		c = append(c, []byte("<font color=\""+color+"\">")...)
	}
	if b {
		c = append(c, []byte("<b>")...)
	}
	if i {
		c = append(c, []byte("<i>")...)
	}
	if u {
		c = append(c, []byte("<u>")...)
	}
	if pos != 0 {
		c = append(c, []byte(fmt.Sprintf(`{\an%d}`, pos))...)
	}
	c = append(c, []byte(escapeHTML(li.Text))...)
	if u {
		c = append(c, []byte("</u>")...)
	}
	if i {
		c = append(c, []byte("</i>")...)
	}
	if b {
		c = append(c, []byte("</b>")...)
	}
	if color != "" {
		c = append(c, []byte("</font>")...)
	}
	return
}
