package turn

import (
	"strings"
	"testing"
)

func TestSplitKeepsEchoWords(t *testing.T) {
	text := "Hello! I'm Atlas. How can I help you today?"
	beats, answer := splitLossless(t, echoChunks(text))
	if len(beats) != 0 || answer != text {
		t.Fatalf("beats %q answer %q", beats, answer)
	}
	help := "I'm Atlas. I can:\n- Run bash commands\n- Read files\n\nWhat would you like me to do?"
	beats, answer = splitLossless(t, echoChunks(help))
	if len(beats) != 0 || answer != help {
		t.Fatalf("help beats %q answer %q", beats, answer)
	}
	demo := "You said: hello there\n\n(I'm a demo agent - set OPENROUTER_API_KEY for full LLM capabilities)"
	beats, answer = splitLossless(t, cutEven(demo, 25))
	if len(beats) != 0 || answer != demo {
		t.Fatalf("cuts beats %q answer %q", beats, answer)
	}
}

func TestSplitStatusTurns(t *testing.T) {
	chunks := []string{
		"Let me check what is available.",
		"The project is on the droplet.",
		"The blog is deployed.\n\n## Status\n\n- Live at the site\n",
	}
	beats, answer := splitLossless(t, chunks)
	if len(beats) != 2 || beats[0] != chunks[0] || beats[1] != chunks[1] {
		t.Fatalf("beats %q", beats)
	}
	if answer != chunks[2] {
		t.Fatalf("answer %q", answer)
	}
}

func TestSplitHeldSentence(t *testing.T) {
	beats, answer := splitLossless(t, []string{"The files are available.", "The", " project is ready."})
	if len(beats) != 1 || beats[0] != "The files are available." {
		t.Fatalf("beats %q", beats)
	}
	if answer != "The project is ready." {
		t.Fatalf("answer %q", answer)
	}
}

func TestSplitKeepsOneMessage(t *testing.T) {
	text := "Error: Permission denied for bash. Approval is required before this tool can run, and it was not executed."
	beats, answer := splitLossless(t, []string{text})
	if len(beats) != 0 || answer != text {
		t.Fatalf("denial beats %q answer %q", beats, answer)
	}
	prose := "The files are ready. The blog is deployed."
	beats, answer = splitLossless(t, []string{prose})
	if len(beats) != 0 || answer != prose {
		t.Fatalf("prose beats %q answer %q", beats, answer)
	}
	beats, answer = splitLossless(t, []string{"The files are ready.", " The blog is deployed."})
	if len(beats) != 1 || beats[0] != "The files are ready. " || answer != "The blog is deployed." {
		t.Fatalf("spaced beats %q answer %q", beats, answer)
	}
}

func TestSplitKeepsCall(t *testing.T) {
	chunks := []string{"Use fmt.", "Println", "("}
	beats, answer := splitLossless(t, chunks)
	if len(beats) != 0 || answer != "Use fmt.Println(" {
		t.Fatalf("beats %q answer %q", beats, answer)
	}
	one := []string{"Use fmt.Println(\"hi\") and stop."}
	beats, answer = splitLossless(t, one)
	if len(beats) != 0 || answer != one[0] {
		t.Fatalf("one shot %q %q", beats, answer)
	}
}

func TestSplitSkipsAbbrevsDotsAndDecimals(t *testing.T) {
	for _, text := range []string{
		"Ask Dr. Smith to look.",
		"See e.g. the note.",
		"Call os.Stdout.Write and wait.",
		"Version 1.2 is current.",
	} {
		beats, answer := splitLossless(t, []string{text})
		if len(beats) != 0 || answer != text {
			t.Fatalf("%q became %q + %q", text, beats, answer)
		}
	}
	beats, answer := splitLossless(t, []string{"1.", "2"})
	if len(beats) != 0 || answer != "1.2" {
		t.Fatalf("decimal %q %q", beats, answer)
	}
	beats, answer = splitLossless(t, []string{"item 2.", "The rest"})
	if len(beats) != 1 || beats[0] != "item 2." || answer != "The rest" {
		t.Fatalf("list end %q %q", beats, answer)
	}
}

func TestSplitSkipsCode(t *testing.T) {
	fenced := "```\nfmt.Println(\n```\n"
	beats, answer := splitLossless(t, []string{fenced})
	if len(beats) != 0 || answer != fenced {
		t.Fatalf("fence %q %q", beats, answer)
	}
	parts := []string{"See `fmt.", "Println` for details."}
	beats, answer = splitLossless(t, parts)
	if len(beats) != 0 || answer != "See `fmt.Println` for details." {
		t.Fatalf("inline %q %q", beats, answer)
	}
	open := []string{"```\ncode.More", " still"}
	beats, answer = splitLossless(t, open)
	if len(beats) != 0 || answer != "```\ncode.More still" {
		t.Fatalf("open fence %q %q", beats, answer)
	}
}

func TestSplitParagraphStays(t *testing.T) {
	text := "The blog is deployed.\n\n## Status\n\n- Live\n"
	beats, answer := splitLossless(t, []string{"The blog is deployed.", "\n\n", "## Status\n\n", "- Live\n"})
	if len(beats) != 0 || answer != text {
		t.Fatalf("paragraph %q %q", beats, answer)
	}
	beats, answer = splitLossless(t, []string{"Alpha beta", " gamma.\n\n", "Next line"})
	if len(beats) != 0 || answer != "Alpha beta gamma.\n\nNext line" {
		t.Fatalf("tail case %q %q", beats, answer)
	}
}

func TestSplitSingleNewline(t *testing.T) {
	beats, answer := splitLossless(t, []string{"Checked the host.\n", "The records are missing."})
	if len(beats) != 1 || strings.TrimSpace(beats[0]) != "Checked the host." || answer != "The records are missing." {
		t.Fatalf("newline %q %q", beats, answer)
	}
}

func TestFlushKeepsTheLastToken(t *testing.T) {
	var s Splitter
	s.Add("Ready.")
	s.Add("Done")
	if s.Answer() != "Ready." || s.Text() != "Ready.Done" {
		t.Fatalf("held answer %q text %q", s.Answer(), s.Text())
	}
	s.Flush()
	if len(s.Beats()) != 0 || s.Answer() != "Ready.Done" {
		t.Fatalf("flush beats %q answer %q", s.Beats(), s.Answer())
	}
}

func TestVisibleStripsAPrefix(t *testing.T) {
	body := "Let me check.The blog is live."
	got := Visible(body, []string{"Let me check."})
	if got != "The blog is live." {
		t.Fatalf("visible %q", got)
	}
	if Visible(body, nil) != body || Visible("nope", []string{"Let me"}) != "nope" {
		t.Fatal("passthrough")
	}
	raw := `["Let me check."]`
	steps := DecodeSteps(raw)
	if len(steps) != 1 || steps[0] != "Let me check." {
		t.Fatalf("steps %#v", steps)
	}
	if DecodeSteps("") != nil || DecodeSteps("nope") != nil {
		t.Fatal("empty steps")
	}
}

func splitLossless(t *testing.T, chunks []string) ([]string, string) {
	t.Helper()
	beats, answer := Split(chunks)
	if strings.Join(beats, "")+answer != strings.Join(chunks, "") {
		t.Fatalf("lossy beats %q answer %q chunks %q", beats, answer, chunks)
	}
	return beats, answer
}

func echoChunks(text string) []string {
	words := strings.Split(text, " ")
	var out []string
	for i, word := range words {
		if i > 0 {
			out = append(out, " ")
		}
		out = append(out, word)
	}
	return out
}

func cutEven(s string, n int) []string {
	var out []string
	for len(s) > 0 {
		if len(s) <= n {
			out = append(out, s)
			break
		}
		out = append(out, s[:n])
		s = s[n:]
	}
	return out
}
