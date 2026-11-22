package main

import (
	"fmt"
	"slices"
	"strings"
)

// ---- Voices ----

// geminiVoice is one of Gemini TTS's prebuilt voices, with Google's
// one-word style for it.
type geminiVoice struct{ name, style string }

// geminiVoices are the voice names Gemini TTS takes, all tried on
// gemini-3.8-flash-tts. OpenRouter has no voices API, and a wrong name
// fails with a bare "Provider returned 400": checkVoice names it first.
var geminiVoices = []geminiVoice{
	{"Umbriel", "easy-going"}, {"Puck", "upbeat"},
	{"Charon", "informative"}, {"Fenrir", "excitable"},
	{"Orus", "firm"}, {"Kore", "firm"}, {"Zephyr", "bright"},
	{"Leda", "youthful"}, {"Aoede", "breezy"},
	{"Callirrhoe", "easy-going"}, {"Autonoe", "bright"},
	{"Enceladus", "breathy"}, {"Iapetus", "clear"},
	{"Algieba", "smooth"}, {"Despina", "smooth"}, {"Erinome", "clear"},
	{"Algenib", "gravelly"}, {"Rasalgethi", "informative"},
	{"Laomedeia", "upbeat"}, {"Achernar", "soft"}, {"Alnilam", "firm"},
	{"Schedar", "even"}, {"Gacrux", "mature"},
	{"Pulcherrima", "forward"}, {"Achird", "friendly"},
	{"Zubenelgenubi", "casual"}, {"Vindemiatrix", "gentle"},
	{"Sadachbia", "lively"}, {"Sadaltager", "knowledgeable"},
	{"Sulafat", "warm"},
}

// isGemini reports whether model is a Gemini TTS, which takes only
// geminiVoices.
func isGemini(model string) bool {
	return strings.HasPrefix(model, "google/gemini")
}

// checkVoice rejects a voice name its model doesn't have, when the
// names are known (Gemini); other models' names pass through.
func checkVoice(v Voice) error {
	if !isGemini(v.Model) || v.Name == "" {
		return nil
	}
	if slices.ContainsFunc(geminiVoices, func(g geminiVoice) bool {
		return g.name == v.Name
	}) {
		return nil
	}
	return fmt.Errorf("voice %q: not a Gemini voice (demo voices)", v.Name)
}

// cmdVoices lists the voices a script's voice name can take.
func cmdVoices(args []string) error {
	if len(args) > 0 {
		fmt.Println("usage: demo voices")
		return nil
	}

	fmt.Printf("Gemini voices (%s, any google/gemini-*-tts), "+
		"by Google's style:\n", defaultVoice.Model)
	for _, g := range geminiVoices {
		mark := ""
		if g.name == defaultVoice.Name {
			mark = "  (default)"
		}
		fmt.Printf("  %-14s %s%s\n", g.name, g.style, mark)
	}
	fmt.Println("\nOther models take their own names (Kokoro am_michael," +
		" Grok Rex, ElevenLabs Liam).\nPick one in a script's or" +
		` scene's voice: {"name": "Charon"}; try it: demo say --voice NAME`)
	return nil
}
