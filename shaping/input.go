// SPDX-License-Identifier: Unlicense OR BSD-3-Clause

package shaping

import (
	"github.com/go-text/typesetting/bidi"
	"github.com/go-text/typesetting/di"
	"github.com/go-text/typesetting/font"
	ot "github.com/go-text/typesetting/font/opentype"
	"github.com/go-text/typesetting/harfbuzz"
	ucd "github.com/go-text/typesetting/internal/unicodedata"
	"github.com/go-text/typesetting/language"
	"github.com/go-text/typesetting/segmenter"
	"golang.org/x/image/math/fixed"
)

type Input struct {
	// Text is the body of text being shaped. Only the range Text[RunStart:RunEnd] is considered
	// for shaping, with the rest provided as context for the shaper. This helps with, for example,
	// cross-run Arabic shaping or handling combining marks at the start of a run.
	Text []rune
	// RunStart and RunEnd indicate the subslice of Text being shaped.
	RunStart, RunEnd int
	// Direction is the directionality of the text.
	Direction di.Direction
	// Face is the font face to render the text in.
	Face *font.Face

	// FontFeatures activates or deactivates optional features
	// provided by the font.
	// The settings are applied to the whole [Text].
	FontFeatures []FontFeature

	// Size is the requested size of the font.
	// More generally, it is a scale factor applied to the resulting metrics.
	// For instance, given a device resolution (in dpi) and a point size (like 14), the `Size` to
	// get result in pixels is given by : pointSize * dpi / 72
	Size fixed.Int26_6

	// Script is an identifier for the writing system used in the text.
	Script language.Script

	// Language is an identifier for the language of the text.
	Language language.Language

	// Level is BIDI embedding level of this run.
	Level bidi.Level
}

// FontFeature sets one font feature.
//
// A font feature is an optionnal behavior a font might expose,
// identified by a 4 bytes [Tag].
// Most features are disabled by default; setting a non zero [Value]
// enables it.
//
// An exemple of font feature is the replacement of fractions (like 1/2, 3/4)
// by specialized glyphs, which would be activated by using
//
//	FontFeature{Tag: ot.MustNewTag("frac"), Value: 1}
//
// See also https://learn.microsoft.com/en-us/typography/opentype/spec/featurelist
// and https://developer.mozilla.org/en-US/docs/Web/CSS/CSS_fonts/OpenType_fonts_guide
type FontFeature struct {
	Tag   ot.Tag
	Value uint32
}

// Fontmap provides a general mechanism to select
// a face to use when shaping text.
type Fontmap interface {
	// ResolveFace is called by [SplitByFace] and [Segmenter.Split] for each input rune potentially
	// triggering a face change.
	// It must always return a valid (non nil) [*font.Face] value.
	ResolveFace(r rune) *font.Face
}

// FontmapScript is an optional interface supporting script hints.
type FontmapScript interface {
	Fontmap

	// SetScript set the script to which the (next) runes passed to [ResolveFace]
	// belong.
	//
	// Providing this information before calling [ResolveFace] is useful for two reasons :
	//	- optimizing [ResolveFace] for runes with a shared script
	//	- for runes with Common and Inherited script, the script resolved by a segmentation step
	//	  is more accurate
	SetScript(language.Script)
}

// FontmapGraphemeCluster is an optional [Fontmap] extension selecting the face
// for a whole grapheme cluster at once.
//
// It is used by [SplitByFace] and [Segmenter.Split] so that a base character
// and its combining marks are always assigned to the same face: the returned
// face should support every rune of the cluster, falling back to another face
// when the preferred one is missing one of the marks.
//
// The runes are a subslice of the input paragraph covering one grapheme
// cluster. As with [Fontmap.ResolveFace], the returned face must never be nil.
type FontmapGraphemeCluster interface {
	Fontmap

	// ResolveFaceForGraphemeCluster resolves the face to use for the given
	// grapheme cluster.
	ResolveFaceForGraphemeCluster(cluster []rune) *font.Face
}

var (
	_ Fontmap                = fixedFontmap(nil)
	_ FontmapGraphemeCluster = fixedFontmap(nil)
)

type fixedFontmap []*font.Face

// ResolveFace panics if the slice is empty
func (ff fixedFontmap) ResolveFace(r rune) *font.Face {
	for _, f := range ff {
		if _, has := f.NominalGlyph(r); has {
			return f
		}
	}
	return ff[0]
}

// ResolveFaceForGraphemeCluster returns the first font providing a nominal
// glyph for every rune of the cluster. If no font covers the whole cluster,
// the first font covering its first rune is used, or simply the first font,
// matching [ResolveFace] for the cluster's base rune.
func (ff fixedFontmap) ResolveFaceForGraphemeCluster(cluster []rune) *font.Face {
	for _, f := range ff {
		supportsAll := true
		for _, r := range cluster {
			if _, has := f.NominalGlyph(r); !has {
				supportsAll = false
				break
			}
		}
		if supportsAll {
			return f
		}
	}
	return ff.ResolveFace(cluster[0])
}

// SplitByFontGlyphs split the runes from 'input' to several items, sharing the same
// characteristics as 'input', expected for the `Face` which is set to
// the first font among 'availableFonts' providing support for all the runes
// in the item.
// Runes supported by no fonts are mapped to the first element of 'availableFonts', which
// must not be empty.
// The 'Face' field of 'input' is ignored: only 'availableFaces' are consulted.
// Rune coverage is obtained by calling the NominalGlyph() method of each font.
// See also [SplitByFace] for a more general approach of font selection.
func SplitByFontGlyphs(input Input, availableFaces []*font.Face) []Input {
	return SplitByFace(input, fixedFontmap(availableFaces))
}

// SplitByFace split the runes from 'input' to several items, sharing the same
// characteristics as 'input', expected for the `Face` which is set to
// the return value of the [Fontmap.ResolveFace] call.
// The 'Face' field of 'input' is ignored: only 'availableFaces' is used to select the face.
//
// Face changes only happen on grapheme cluster boundaries, so combining marks
// are always kept in the same run, and shaped by the same font, as their base
// character.
func SplitByFace(input Input, availableFaces Fontmap) []Input {
	var graphemes segmenter.Segmenter
	graphemes.Init(input.Text)
	return splitByFace(input, availableFaces, nil, true, &graphemes)
}

// Segmenter holds a state used to split input
// according to three caracteristics : text direction (bidi),
// script, and face.
type Segmenter struct {
	// pools of inputs, used to reduce allocations,
	// which are alternatively swapped between each step of the segmentation
	input, output []Input

	// used to handle Common script
	delimStack []delimEntry

	// buffer used for bidi segmentation
	bidiParagraph bidi.Paragraph

	// graphemes computes the grapheme cluster boundaries used to make sure
	// face changes never split a grapheme cluster.
	graphemes segmenter.Segmenter
}

type delimEntry struct {
	index  int             // in the [pairedDelims] list
	script language.Script // resolved from the context
}

// Split segments the given pre-configured input according to:
//   - text direction
//   - script
//   - language
//   - (vertical text only) glyph orientation
//   - face, as defined by [faces]
//
// Only the input runes in the range [text.RunStart] to [text.RunEnd] will be split.
//
// As a consequence, it sets the following fields of the returned runs:
//   - Text, RunStart, RunEnd
//   - Direction
//   - Script
//   - Language
//   - Face
//   - Level
//
// [text.Direction] is used during bidi ordering, and should refer to the general
// context [text] is used in (typically the user system preference for GUI apps.)
//
// For vertical text, if its orientation is set, is copied as it is; otherwise, the
// orientation is resolved using the Unicode recommendations (see https://www.unicode.org/reports/tr50/).
//
// When possible, the language are resolved to match the current script. For instance,
// (language: 'fr', script: 'arabic') is resolved to language: 'arabic'.
//
// The returned sliced is owned by the [Segmenter] and is only valid until
// the next call to [Split].
func (seg *Segmenter) Split(text Input, faces Fontmap) []Input {
	seg.reset()
	seg.splitByBidi(text) // fills output

	seg.input, seg.output = seg.output, seg.input
	seg.output = seg.output[:0] // reset but keep underlyng storage
	seg.splitByScript()

	seg.enforceLanguages()

	// if needed, resolve text orientation for vertical text
	if text.Direction.IsVertical() && !text.Direction.HasVerticalOrientation() {
		seg.input, seg.output = seg.output, seg.input
		seg.output = seg.output[:0] // reset but keep underlyng storage
		seg.splitByVertOrientation()
	}

	seg.input, seg.output = seg.output, seg.input
	seg.output = seg.output[:0]
	// compute the grapheme cluster boundaries of the whole paragraph once,
	// so that [splitByFace] can keep each cluster in a single face
	seg.graphemes.Init(text.Text)
	seg.splitByFace(faces)

	return seg.output
}

func (seg *Segmenter) reset() {
	// zero the slices to avoid 'memory leak' on pointer slice fields
	for i := range seg.input {
		seg.input[i].Text = nil
		seg.input[i].FontFeatures = nil
	}
	for i := range seg.output {
		seg.output[i].Text = nil
		seg.output[i].FontFeatures = nil
	}
	seg.input = seg.input[:0]
	seg.output = seg.output[:0]

	seg.delimStack = seg.delimStack[:0]
}

// we split vertical text like horizontal one
func (seg *Segmenter) splitByBidi(text Input) {
	// do nothing for empty runs
	if text.RunStart >= text.RunEnd {
		seg.output = append(seg.output, text)
		return
	}

	def := bidi.LeftToRight
	if text.Direction.Progression() == di.TowardTopLeft {
		def = bidi.RightToLeft
	}

	// our BIDI implementation does not handle multiple paragraphs
	currentStart := text.RunStart
	for i := text.RunStart; i < text.RunEnd; i++ {
		if ucd.IsBidiB(text.Text[i]) {
			// we have a break : keep the separator on this
			// paragraph and create a new run
			newRun := text
			newRun.RunStart, newRun.RunEnd = currentStart, i+1
			currentStart = i + 1
			seg.input = append(seg.input, newRun)
		}
	}
	// flush the last run, if not empty
	if currentStart < text.RunEnd {
		newRun := text
		newRun.RunStart = currentStart
		seg.input = append(seg.input, newRun)
	}

	// apply BIDI on each paragraph
	for _, inputRun := range seg.input {
		out := seg.bidiParagraph.Segment(inputRun.Text[inputRun.RunStart:inputRun.RunEnd], def)
		if out.NumRuns() == 0 {
			seg.output = append(seg.output, inputRun)
			continue
		}

		input := inputRun // start at rune 0 of the run
		for i := 0; i < out.NumRuns(); i++ {
			currentInput := input
			innerRun := out.Run(i)

			currentInput.RunEnd = innerRun.End + inputRun.RunStart // shift by the input run position
			currentInput.Level = innerRun.Level

			// override the direction
			if innerRun.IsLeftToRight() {
				currentInput.Direction.SetProgression(di.FromTopLeft)
			} else {
				currentInput.Direction.SetProgression(di.TowardTopLeft)
			}

			seg.output = append(seg.output, currentInput)
			input.RunStart = currentInput.RunEnd
		}
	}
}

// lookupDelimIndex binary searches in the list of the paired delimiters,
// and returns -1 if `ch` is not found
func lookupDelimIndex(ch rune) int {
	lower := 0
	upper := len(pairedDelims) - 1

	for lower <= upper {
		mid := (lower + upper) / 2

		if ch < pairedDelims[mid] {
			upper = mid - 1
		} else if ch > pairedDelims[mid] {
			lower = mid + 1
		} else {
			return mid
		}
	}

	return -1
}

// See https://unicode.org/reports/tr24/#Common for reference
func (seg *Segmenter) splitByScript() {
	for _, input := range seg.input {
		currentInput := input
		currentInput.Script = language.Common

		for i := input.RunStart; i < input.RunEnd; i++ {
			r := input.Text[i]
			rScript := language.LookupScript(r)

			// to properly handle Common script,
			// we register paired delimiters

			delimIndex := -1
			if !rScript.Strong() {
				delimIndex = lookupDelimIndex(r)
			}

			if delimIndex >= 0 { // handle paired characters
				if delimIndex%2 == 0 {
					// this is an open character : push it onto the stack
					seg.delimStack = append(seg.delimStack, delimEntry{delimIndex, currentInput.Script})
				} else {
					// this is a close character : try to look backward in the stack
					// for its counterpart
					counterPartIndex := delimIndex - 1
					j := len(seg.delimStack) - 1
					for ; j >= 0; j-- {
						if seg.delimStack[j].index == counterPartIndex { // found a match, use its script
							rScript = seg.delimStack[j].script
							break
						}
					}
					// in any case, pop the open characters
					if j == -1 {
						j = 0
					}
					seg.delimStack = seg.delimStack[:j]
				}
			}

			// check if we have a 'real' change of script, or not
			if !rScript.Strong() || rScript == currentInput.Script {
				// no change
				continue
			} else if currentInput.Script == language.Common {
				// update the pair stack to attribute the resolved script
				for i := range seg.delimStack {
					seg.delimStack[i].script = rScript
				}
				// set the resolved script to the current run,
				// but do NOT create a new run
				currentInput.Script = rScript
			} else {
				// split to a new run
				if i != input.RunStart { // push the existing one
					currentInput.RunEnd = i
					seg.output = append(seg.output, currentInput)
				}

				currentInput.RunStart = i
				currentInput.Script = rScript
			}
		}
		// close and add the last input
		currentInput.RunEnd = input.RunEnd
		seg.output = append(seg.output, currentInput)
	}
}

// assume splitByScript has been called and enforce sane languages
func (seg *Segmenter) enforceLanguages() {
	initialLang := seg.output[0].Language

	// if no language is specified, use a default
	// known by the library
	if initialLang == "" {
		initialLang = "en"
	}

	initialLangID, ok := language.NewLangID(initialLang)
	if !ok {
		// the language is unknown to the library, nothing to do
		return
	}

	for i, run := range seg.output {
		resolved := enforceLang(initialLangID, run.Script)
		seg.output[i].Language = resolved.Language()
	}
}

// assume the script has been resolved
func (seg *Segmenter) splitByVertOrientation() {
	for _, input := range seg.input {
		vo := ucd.LookupVerticalOrientation(input.Script)
		currentInput := input

		for i := input.RunStart; i < input.RunEnd; i++ {
			r := input.Text[i]
			sideways := vo.Orientation(r)
			if i == input.RunStart {
				// first run : update the orientation,
				// but do not create a new run
				currentInput.Direction.SetSideways(sideways)
				continue
			}

			if sideways != currentInput.Direction.IsSideways() {
				// create new run : push the current one ...
				currentInput.RunEnd = i
				seg.output = append(seg.output, currentInput)

				// ... and update the 'new'
				currentInput.RunStart = i
				currentInput.Direction.SetSideways(sideways)
			}
		}

		// close and add the last input
		currentInput.RunEnd = input.RunEnd
		seg.output = append(seg.output, currentInput)
	}
}

// assume [splitByScript] has been called and [graphemes] has been
// initialized with the whole paragraph
func (seg *Segmenter) splitByFace(faces Fontmap) {
	withScript, hasScriptSupport := faces.(FontmapScript)
	clusterFontmap, hasClusterSupport := faces.(FontmapGraphemeCluster)

	if len(seg.input) == 0 {
		return
	}
	text := seg.input[0].Text
	paragraphStart := seg.input[0].RunStart
	paragraphEnd := seg.input[len(seg.input)-1].RunEnd
	seg.output = seg.output[:0]

	// runCursor indexes the upstream run (from bidi/script/orientation
	// splitting) containing the current grapheme cluster; scriptCursor is the
	// last upstream run whose script hint has been pushed to the font map
	runCursor, scriptCursor := 0, -1
	// current is the output run being built, or nil before the first cluster
	var current *Input

	clusterStart := paragraphStart
	for clusterStart < paragraphEnd {
		// end of the grapheme cluster starting at clusterStart
		clusterEnd := clusterStart + 1
		for clusterEnd < paragraphEnd && !seg.graphemes.IsGraphemeBoundary(clusterEnd) {
			clusterEnd++
		}

		// move to the upstream run containing the first rune of the cluster,
		// tracking whether an upstream boundary was crossed
		upstreamChanged := false
		for runCursor < len(seg.input) && seg.input[runCursor].RunEnd <= clusterStart {
			runCursor++
			upstreamChanged = true
		}
		upstream := seg.input[runCursor]
		// the font map may use the script hint to resolve a face, so push it
		// before the first resolution of each upstream run, including the first
		if hasScriptSupport && runCursor != scriptCursor {
			withScript.SetScript(upstream.Script)
			scriptCursor = runCursor
		}
		// a cluster spills when an earlier stage split it from one of its marks,
		// so that it spans more than one upstream run; such a boundary must be
		// absorbed rather than split the cluster
		spills := clusterEnd > upstream.RunEnd

		firstRune := text[clusterStart]
		if ignoreFaceChange(firstRune) {
			// ignorable runes (spaces, controls, format characters such as ZWJ)
			// do not select a face; they extend the current run, opening a face
			// less one only when they start a new upstream run. Their face is
			// resolved later, by back filling from the following face run, or by
			// resolving the last rune for an all ignorable paragraph.
			if current == nil {
				in := upstream
				in.RunStart, in.RunEnd = clusterStart, clusterEnd
				seg.output = append(seg.output, in)
				current = &seg.output[len(seg.output)-1]
			} else if upstreamChanged && !spills {
				in := upstream
				in.RunStart, in.RunEnd = clusterStart, clusterEnd
				seg.output = append(seg.output, in)
				current = &seg.output[len(seg.output)-1]
			} else {
				current.RunEnd = clusterEnd
			}
			clusterStart = clusterEnd
			continue
		}

		// resolve the face for the whole grapheme cluster at once
		var selectedFace *font.Face
		if hasClusterSupport {
			selectedFace = clusterFontmap.ResolveFaceForGraphemeCluster(text[clusterStart:clusterEnd])
		} else {
			selectedFace = faces.ResolveFace(firstRune)
		}

		switch {
		case current == nil:
			// first face of the paragraph
			in := upstream
			in.RunStart, in.RunEnd = clusterStart, clusterEnd
			in.Face = selectedFace
			seg.output = append(seg.output, in)
			current = &seg.output[len(seg.output)-1]
		case spills:
			// the cluster spans an upstream boundary: keep it whole in the run
			// it starts in and give that run the resolved face if needed
			current.RunEnd = clusterEnd
			if current.Face == nil {
				current.Face = selectedFace
			}
		case upstreamChanged:
			// a real upstream boundary on a grapheme cluster boundary: new run
			in := upstream
			in.RunStart, in.RunEnd = clusterStart, clusterEnd
			in.Face = selectedFace
			seg.output = append(seg.output, in)
			current = &seg.output[len(seg.output)-1]
		case current.Face == nil:
			// first face after leading face less runes in the same upstream run:
			// extend it and assign the face
			current.RunEnd = clusterEnd
			current.Face = selectedFace
		case current.Face != selectedFace:
			// an actual face change: new run carrying the current properties
			in := *current
			in.RunStart, in.RunEnd = clusterStart, clusterEnd
			in.Face = selectedFace
			seg.output = append(seg.output, in)
			current = &seg.output[len(seg.output)-1]
		default:
			// same face and same upstream run: keep accumulating clusters
			current.RunEnd = clusterEnd
		}

		clusterStart = clusterEnd
	}

	switch {
	case current == nil:
		// empty paragraph: emit a single face less run
		in := seg.input[0]
		in.RunStart, in.RunEnd = paragraphStart, paragraphEnd
		seg.output = append(seg.output, in)
	case current.Face == nil:
		// the paragraph only contains ignorable runes: resolve a face on its
		// last rune, like [splitByFace] does for its final run
		current.Face = faces.ResolveFace(text[paragraphEnd-1])
	}

	// back fill the face less runs (spaces, controls, ZWJ...) with the face of
	// the run immediately following them
	for i := len(seg.output) - 2; i >= 0; i-- {
		if seg.output[i].Face == nil {
			seg.output[i].Face = seg.output[i+1].Face
		}
	}
}

// splitByFace split the runes from 'input' to several items, sharing the same
// characteristics as 'input', except for the `Face` which is set to
// the return value of the [Fontmap.ResolveFace] call.
//
// Face selection is performed per grapheme cluster, never per rune: a cluster
// is always assigned entirely to one face, so that combining marks stay with
// their base character and are shaped by the same font. Grapheme cluster
// boundaries are read from 'graphemes', which must have been initialized with
// the whole paragraph containing [input.Text].
func splitByFace(input Input, availableFaces Fontmap, buffer []Input, isLast bool, graphemes *segmenter.Segmenter) []Input {
	// an empty run is returned unchanged and keeps its nil face
	if input.RunStart >= input.RunEnd {
		return append(buffer, input)
	}

	clusterFontmap, hasClusterSupport := availableFaces.(FontmapGraphemeCluster)
	currentInput := input
	clusterStart := input.RunStart
	for clusterStart < input.RunEnd {
		// find the end of the grapheme cluster starting at clusterStart
		clusterEnd := clusterStart + 1
		for clusterEnd < input.RunEnd && !graphemes.IsGraphemeBoundary(clusterEnd) {
			clusterEnd++
		}

		firstRune := input.Text[clusterStart]
		// We can safely ignore characters if we have a face or if there is more text,
		// but we must force the choice of a face if we still don't have one and we reach
		// the final rune. Otherwise strings like all-whitespace are never assigned a face.
		if ignoreFaceChange(firstRune) && (currentInput.Face != nil || !isLast || clusterEnd < input.RunEnd) {
			// add the cluster to the current input
			clusterStart = clusterEnd
			continue
		}

		// select the face for the whole grapheme cluster at once, so that a base
		// character and its combining marks are never split between two faces;
		// font maps without cluster support resolve the face through the first
		// rune of the cluster, which is itself always a cluster boundary
		var selectedFace *font.Face
		if hasClusterSupport {
			selectedFace = clusterFontmap.ResolveFaceForGraphemeCluster(input.Text[clusterStart:clusterEnd])
		} else {
			selectedFace = availableFaces.ResolveFace(firstRune)
		}

		// now that we have a font, apply it back,
		// but do NOT create a new run
		if currentInput.Face == nil {
			currentInput.Face = selectedFace
		}

		if currentInput.Face == selectedFace {
			// add the cluster to the current input
			clusterStart = clusterEnd
			continue
		}

		// new face needed

		if clusterStart != input.RunStart {
			// close the current input ...
			currentInput.RunEnd = clusterStart
			// ... add it to the output ...
			buffer = append(buffer, currentInput)
		}

		// ... and create a new one
		currentInput = input
		currentInput.RunStart = clusterStart
		currentInput.Face = selectedFace

		clusterStart = clusterEnd
	}

	// close and add the last input
	currentInput.RunEnd = input.RunEnd
	buffer = append(buffer, currentInput)
	return buffer
}

// ignoreFaceChange returns `true` is the given rune should not trigger
// a change of font.
//
// We don't want space characters to affect font selection; in general,
// it's always wrong to select a font just to render a space.
// We assume that all fonts have the ASCII space, and for other space
// characters if they don't, HarfBuzz will compatibility-decompose them
// to ASCII space...
//
// We don't want to change fonts for line or paragraph separators.
//
// Finaly, we also don't change fonts for what Harfbuzz consider
// as ignorable (however, some Control Format runes like 06DD are not ignored).
//
// The rationale is taken from pango : see bugs
// https://bugzilla.gnome.org/show_bug.cgi?id=355987
// https://bugzilla.gnome.org/show_bug.cgi?id=701652
// https://bugzilla.gnome.org/show_bug.cgi?id=781123
// for more details.
func ignoreFaceChange(r rune) bool {
	g := ucd.LookupGeneralCategory(r)
	return g == ucd.Cc || // control
		g == ucd.Cs || // surrogate
		g == ucd.Zl || // line separator
		g == ucd.Zp || // paragraph separator
		(g == ucd.Zs && r != '\u1680') || // space separator != OGHAM SPACE MARK
		harfbuzz.IsDefaultIgnorable(r)
}

// enforceLang makes sure the returned language is compatible with
// the given script, so that it maybe be usefull for Opentype shaping.
//
// A typical example is a Korean text found into a French paragraph :
//
//	enforceLang("fr", language.Hangul) -> "ko"
func enforceLang(lang language.LangID, s language.Script) language.LangID {
	if lang.UseScript(s) {
		// the language is consistent with the script, return it
		return lang
	}

	// the language is not used for the script, try to replace it
	if l := language.ScriptToLang[s]; l != 0 {
		return l
	}

	// we do not have good replacement, leave it as it is
	return lang
}
