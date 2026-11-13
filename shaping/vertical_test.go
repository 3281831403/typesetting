// SPDX-License-Identifier: Unlicense OR BSD-3-Clause

package shaping

import (
	"fmt"
	"testing"

	"github.com/go-text/typesetting/di"
	"github.com/go-text/typesetting/font"
	"github.com/go-text/typesetting/language"
	tu "github.com/go-text/typesetting/testutils"
	"golang.org/x/image/math/fixed"
)

// verticalWrappingParagraph is long enough to require several columns when
// wrapped to a short column height in either vertical direction.
var verticalWrappingParagraph = []rune("Hello world this is a fairly long paragraph that should wrap into several columns given a short column height")

func verticalInput(t *testing.T, face *font.Face, dir di.Direction, text []rune) Input {
	t.Helper()
	return Input{
		Text:      text,
		RunStart:  0,
		RunEnd:    len(text),
		Direction: dir,
		Face:      face,
		Size:      fixed.I(160),
		Script:    language.Latin,
		Language:  language.NewLanguage("EN"),
	}
}

// lineLength is the test-side mirror of Output.lineLength: it sums the
// progression-direction length of every run in the line and is therefore
// non-negative for both horizontal lines and vertical columns.
func lineLength(line Line) fixed.Int26_6 {
	var total fixed.Int26_6
	for _, run := range line {
		if run.Direction.IsVertical() {
			total += -run.Advance
		} else {
			total += run.Advance
		}
	}
	return total
}

// TestVerticalWrappingColumnLength ensures vertical text is broken into several
// columns and that no column ever exceeds the requested height. Before the fix,
// vertical advances are negative so the signed comparison treated every column
// as fitting and the whole paragraph stayed in one column.
func TestVerticalWrappingColumnLength(t *testing.T) {
	face := loadOpentypeFont(t, "../font/testdata/Roboto-Regular.ttf")
	shaper := &HarfbuzzShaper{}
	const maxHeight = 5000

	for _, dir := range []di.Direction{di.DirectionTTB, di.DirectionBTT} {
		t.Run(fmt.Sprintf("%d", dir), func(t *testing.T) {
			out := shaper.Shape(verticalInput(t, face, dir, verticalWrappingParagraph))
			tu.Assert(t, out.Direction.IsVertical())
			tu.Assert(t, out.Advance < 0) // vertical advances stay signed in the shaped output

			var wrapper LineWrapper
			lines, _ := wrapper.WrapParagraph(
				WrapConfig{Direction: dir},
				maxHeight,
				verticalWrappingParagraph,
				NewSliceIterator([]Output{out}),
			)

			tu.Assert(t, len(lines) > 1)
			for i, line := range lines {
				length := lineLength(line)
				if length < 0 {
					t.Fatalf("line %d has negative progression length %d", i, length)
				}
				if length > fixed.I(maxHeight) {
					t.Fatalf("line %d length %d exceeds column height %d", i, length, maxHeight)
				}
			}
		})
	}
}

// TestVerticalWrappingTruncation ensures truncation actually fires for vertical
// text. Before the fix the negative signed advances meant the truncation budget
// was never exceeded.
func TestVerticalWrappingTruncation(t *testing.T) {
	face := loadOpentypeFont(t, "../font/testdata/Roboto-Regular.ttf")
	shaper := &HarfbuzzShaper{}
	const maxHeight = 5000

	for _, dir := range []di.Direction{di.DirectionTTB, di.DirectionBTT} {
		t.Run(fmt.Sprintf("%d", dir), func(t *testing.T) {
			out := shaper.Shape(verticalInput(t, face, dir, verticalWrappingParagraph))
			truncator := shaper.Shape(verticalInput(t, face, dir, []rune("…")))

			var wrapper LineWrapper
			lines, truncated := wrapper.WrapParagraph(
				WrapConfig{
					Direction:          dir,
					TruncateAfterLines: 1,
					Truncator:          truncator,
				},
				maxHeight,
				verticalWrappingParagraph,
				NewSliceIterator([]Output{out}),
			)

			tu.Assert(t, len(lines) == 1)
			if truncated <= 0 {
				t.Fatalf("expected truncation for %v text, got %d runes", dir, truncated)
			}
			// the truncator must be the final visual run and its length must
			// have been reserved within the column height
			tu.Assert(t, lineLength(lines[0]) <= fixed.I(maxHeight))
			lastRun := lines[0][len(lines[0])-1]
			tu.Assert(t, lastRun.Runes.Count == truncated)
		})
	}
}

// TestVerticalSpacingLengthensColumns ensures positive word and letter spacing
// makes vertical columns longer (so fewer glyphs fit per column). Before the
// fix, adding a positive spacing to negative advances shortened the signed
// advance, so spacing paradoxically made columns shorter.
func TestVerticalSpacingLengthensColumns(t *testing.T) {
	face := loadOpentypeFont(t, "../font/testdata/Roboto-Regular.ttf")
	shaper := &HarfbuzzShaper{}
	const maxHeight = 5000

	for _, dir := range []di.Direction{di.DirectionTTB, di.DirectionBTT} {
		t.Run(fmt.Sprintf("%d", dir), func(t *testing.T) {
			plain := shaper.Shape(verticalInput(t, face, dir, verticalWrappingParagraph))
			plainLength := -plain.Advance

			spacedRuns := []Output{shaper.Shape(verticalInput(t, face, dir, verticalWrappingParagraph))}
			AddSpacing(spacedRuns, verticalWrappingParagraph, fixed.I(200), fixed.I(40))
			spaced := spacedRuns[0]
			spacedLength := -spaced.Advance

			if spacedLength <= plainLength {
				t.Fatalf("positive spacing did not lengthen the column: plain=%d spaced=%d", plainLength, spacedLength)
			}

			var plainWrapper, spacedWrapper LineWrapper
			plainLines, _ := plainWrapper.WrapParagraph(
				WrapConfig{Direction: dir}, maxHeight, verticalWrappingParagraph,
				NewSliceIterator([]Output{plain}),
			)
			spacedLines, _ := spacedWrapper.WrapParagraph(
				WrapConfig{Direction: dir}, maxHeight, verticalWrappingParagraph,
				NewSliceIterator(spacedRuns),
			)
			if len(spacedLines) <= len(plainLines) {
				t.Fatalf("spacing did not produce more columns: plain=%d spaced=%d", len(plainLines), len(spacedLines))
			}
			for i, line := range spacedLines {
				if length := lineLength(line); length > fixed.I(maxHeight) {
					t.Fatalf("spaced line %d length %d exceeds column height %d", i, length, maxHeight)
				}
			}
		})
	}
}

// TestVerticalTrimmedTrailingWhitespace ensures the trimmed trailing whitespace
// metric is always non-negative for vertical text and equals the positive length
// of the collapsed whitespace. Before the fix it was computed from signed
// advances and came out negative.
func TestVerticalTrimmedTrailingWhitespace(t *testing.T) {
	face := loadOpentypeFont(t, "../font/testdata/Roboto-Regular.ttf")
	shaper := &HarfbuzzShaper{}
	text := []rune("trailing spaces here      ")

	for _, dir := range []di.Direction{di.DirectionTTB, di.DirectionBTT} {
		t.Run(fmt.Sprintf("%d", dir), func(t *testing.T) {
			out := shaper.Shape(verticalInput(t, face, dir, text))

			var wrapper LineWrapper
			wrapper.Prepare(WrapConfig{Direction: dir}, text, NewSliceIterator([]Output{out}))
			line, _ := wrapper.WrapNextLine(100000)

			if line.TrimmedTrailingWhitespace < 0 {
				t.Fatalf("TrimmedTrailingWhitespace must be non-negative, got %d", line.TrimmedTrailingWhitespace)
			}
			if line.TrimmedTrailingWhitespace == 0 {
				t.Fatalf("expected trailing whitespace to be trimmed for %v text", dir)
			}
			// The reported trim must match the actual advance removed from the
			// visual-final whitespace glyph.
			finalRun := line.Line[0]
			var finalGlyph *Glyph
			if dir.Progression() == di.FromTopLeft {
				finalGlyph = &finalRun.Glyphs[len(finalRun.Glyphs)-1]
			} else {
				finalGlyph = &finalRun.Glyphs[0]
			}
			tu.Assert(t, finalGlyph.Advance == 0)
		})
	}
}

// TestVerticalLetterSpacingTrimRoundTrip ensures trimStartLetterSpacing is the
// exact inverse of the start-of-cluster letter spacing applied by
// AddLetterSpacing for vertical runs. cutRun relies on this to drop the half
// spacing at the start of each wrapped column; with the old sign convention the
// trim moved the glyph the wrong way and left its advance modified.
func TestVerticalLetterSpacingTrimRoundTrip(t *testing.T) {
	face := loadOpentypeFont(t, "../font/testdata/Roboto-Regular.ttf")
	shaper := &HarfbuzzShaper{}
	text := []rune("Hello world")
	halfSpacing := fixed.I(8) / 2

	for _, dir := range []di.Direction{di.DirectionTTB, di.DirectionBTT} {
		t.Run(fmt.Sprintf("%d", dir), func(t *testing.T) {
			run := shaper.Shape(verticalInput(t, face, dir, text))
			first := &run.Glyphs[0]
			originalAdvance, originalYAdvance, originalYOffset := first.Advance, first.YAdvance, first.YOffset

			// reproduce the start-of-cluster half spacing from AddLetterSpacing
			advanceDelta := spacingAdvanceDelta(dir, halfSpacing)
			offsetDelta := spacingOffsetDelta(dir, halfSpacing)
			first.Advance += advanceDelta
			first.YAdvance += advanceDelta
			first.YOffset += offsetDelta
			first.startLetterSpacing += halfSpacing

			// trimStartLetterSpacing must undo it exactly
			run.trimStartLetterSpacing()
			tu.Assert(t, first.Advance == originalAdvance)
			tu.Assert(t, first.YAdvance == originalYAdvance)
			tu.Assert(t, first.YOffset == originalYOffset)
			tu.Assert(t, first.startLetterSpacing == 0)
		})
	}
}

// TestVerticalLineLengthMetric documents the Output.lineLength convention
// directly: positive along the progression for every direction.
func TestVerticalLineLengthMetric(t *testing.T) {
	face := loadOpentypeFont(t, "../font/testdata/Roboto-Regular.ttf")
	shaper := &HarfbuzzShaper{}
	text := []rune("vertical")

	for _, dir := range []di.Direction{di.DirectionLTR, di.DirectionRTL, di.DirectionTTB, di.DirectionBTT} {
		out := shaper.Shape(verticalInput(t, face, dir, text))
		length := out.lineLength()
		if length <= 0 {
			t.Fatalf("%v: lineLength must be positive, got %d (Advance=%d)", dir, length, out.Advance)
		}
		if out.Direction.IsVertical() {
			tu.Assert(t, length == -out.Advance)
		} else {
			tu.Assert(t, length == out.Advance)
		}
	}
}
