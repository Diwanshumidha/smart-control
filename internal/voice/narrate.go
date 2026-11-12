package voice

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// SampleRate is the rate every take is written at.
const SampleRate = 48000

// Natural gaps between two spoken pieces when the script doesn't say
// [pause …]: after a sentence, after a clause, and mid-phrase (a cue split).
const (
	gapSentence = 0.38
	gapClause   = 0.18
	gapPhrase   = 0.05
	maxPause    = 3.0
)

// Piece is one part of a narration line: text to speak, a pause, or a cue.
type Piece struct {
	Text  string  // spoken text (Kind "text")
	Pause float64 // seconds of silence (Kind "pause")
	Cue   string  // cue name (Kind "cue")
	Kind  string
}

var markRe = regexp.MustCompile(`\[pause(?:\s+([0-9]*\.?[0-9]+)\s*s?)?\]|\{([A-Za-z0-9_-]+)\}`)

// Parse splits a narration line into pieces. "[pause]" is 0.5 s, "[pause 0.8]"
// is 0.8 s; "{name}" marks the moment the next word starts (a cue the picture
// syncs to).
func Parse(line string) ([]Piece, error) {
	var out []Piece
	addText := func(s string) {
		s = strings.Join(strings.Fields(s), " ")
		if s != "" {
			out = append(out, Piece{Kind: "text", Text: s})
		}
	}
	last := 0
	for _, m := range markRe.FindAllStringSubmatchIndex(line, -1) {
		addText(line[last:m[0]])
		last = m[1]
		switch {
		case m[4] >= 0:
			out = append(out, Piece{Kind: "cue", Cue: line[m[4]:m[5]]})
		default:
			d := 0.5
			if m[2] >= 0 {
				d, _ = strconv.ParseFloat(line[m[2]:m[3]], 64)
			}
			if d <= 0 || d > maxPause {
				return nil, fmt.Errorf("pause must be between 0 and %.0f s: %q", maxPause, line[m[0]:m[1]])
			}
			out = append(out, Piece{Kind: "pause", Pause: d})
		}
	}
	addText(line[last:])
	if rest := markRe.ReplaceAllString(line, ""); strings.ContainsAny(rest, "[]{}") {
		return nil, fmt.Errorf("unknown marker in %q: use [pause], [pause 0.6] or {cue-name}", line)
	}
	hasText := false
	for _, p := range out {
		hasText = hasText || p.Kind == "text"
	}
	if !hasText {
		return nil, fmt.Errorf("nothing to say in %q", line)
	}
	return out, nil
}

// Spoken returns the line without markers, as it will be heard.
func Spoken(line string) string {
	return strings.Join(strings.Fields(markRe.ReplaceAllString(line, " ")), " ")
}

// Cue is a named moment inside a take, in seconds from its start.
type Cue struct {
	Name string  `json:"name"`
	At   float64 `json:"at"`
}

// Span is one spoken piece inside a take.
type Span struct {
	Text  string  `json:"text"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
}

// Take is a finished narration line: a WAV file starting on the first word
// and ending on the last.
type Take struct {
	Path     string  `json:"path"`
	Duration float64 `json:"duration"`
	Cues     []Cue   `json:"cues,omitempty"`
	Spans    []Span  `json:"spans"`
}

// Narrate speaks line with p into out. Pieces are synthesised separately,
// trimmed to the speech and joined with exact gaps, so pauses are what the
// script says and cue times are exact. cacheDir keeps raw pieces so a re-run
// only synthesises text that changed.
func Narrate(ctx context.Context, p Provider, provider, line, voiceID string, speed float64, out, cacheDir string) (*Take, error) {
	pieces, err := Parse(line)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return nil, err
	}
	var pcm []int16
	take := &Take{Path: out}
	pendingGap := -1.0 // explicit pause before the next text, -1 = natural
	var pendingCues []string
	prevText := ""
	for _, pc := range pieces {
		switch pc.Kind {
		case "pause":
			pendingGap = math.Max(pendingGap, 0) + pc.Pause
		case "cue":
			pendingCues = append(pendingCues, pc.Cue)
		case "text":
			samples, err := piece(ctx, p, provider, pc.Text, voiceID, speed, cacheDir)
			if err != nil {
				return nil, err
			}
			if prevText != "" || pendingGap > 0 {
				gap := pendingGap
				if gap < 0 {
					gap = naturalGap(prevText)
				}
				pcm = append(pcm, make([]int16, int(gap*SampleRate))...)
			}
			start := seconds(len(pcm))
			for _, c := range pendingCues {
				take.Cues = append(take.Cues, Cue{Name: c, At: round(start)})
			}
			pendingCues, pendingGap = nil, -1
			pcm = append(pcm, samples...)
			take.Spans = append(take.Spans, Span{Text: pc.Text, Start: round(start), End: round(seconds(len(pcm)))})
			prevText = pc.Text
		}
	}
	for _, c := range pendingCues { // cue after the last word: the end of the take
		take.Cues = append(take.Cues, Cue{Name: c, At: round(seconds(len(pcm)))})
	}
	if pendingGap > 0 { // trailing pause is kept as silence
		pcm = append(pcm, make([]int16, int(pendingGap*SampleRate))...)
	}
	take.Duration = round(seconds(len(pcm)))
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return nil, err
	}
	return take, writeWAV(out, pcm)
}

func naturalGap(prev string) float64 {
	r := []rune(strings.TrimSpace(prev))
	if len(r) == 0 {
		return gapPhrase
	}
	switch last := r[len(r)-1]; {
	case strings.ContainsRune(".!?…", last):
		return gapSentence
	case strings.ContainsRune(",;:—–-", last):
		return gapClause
	case unicode.IsPunct(last):
		return gapClause
	}
	return gapPhrase
}

// piece synthesises one text piece (cached) and returns it decoded and
// trimmed to the speech.
func piece(ctx context.Context, p Provider, provider, text, voiceID string, speed float64, cacheDir string) ([]int16, error) {
	h := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%.3f|%s", provider, voiceID, speed, text)))
	raw := filepath.Join(cacheDir, hex.EncodeToString(h[:8])+".wav")
	if st, err := os.Stat(raw); err != nil || st.Size() == 0 {
		tmp := raw + ".part.wav"
		if err := p.Synthesize(ctx, Request{Text: text, Voice: voiceID, Speed: speed, Out: tmp}); err != nil {
			os.Remove(tmp)
			return nil, fmt.Errorf("speaking %q: %w", text, err)
		}
		if err := os.Rename(tmp, raw); err != nil {
			return nil, err
		}
	}
	samples, err := decode(ctx, raw)
	if err != nil {
		return nil, err
	}
	return trim(samples), nil
}

// decode reads any audio file as mono 16-bit PCM at SampleRate.
func decode(ctx context.Context, path string) ([]int16, error) {
	cmd := exec.CommandContext(ctx, "ffmpeg", "-v", "error", "-i", path, "-f", "s16le", "-ac", "1", "-ar", strconv.Itoa(SampleRate), "-")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg decode %s: %v %s", filepath.Base(path), err, strings.TrimSpace(stderr.String()))
	}
	b := stdout.Bytes()
	out := make([]int16, len(b)/2)
	for i := range out {
		out[i] = int16(binary.LittleEndian.Uint16(b[2*i:]))
	}
	return out, nil
}

// trim cuts leading and trailing silence, keeping a little air so word
// onsets and tails aren't clipped, and fades the edges to avoid clicks.
func trim(s []int16) []int16 {
	const (
		win       = SampleRate / 100 // 10 ms
		threshold = 0.008            // ≈ -42 dBFS RMS
		padIn     = SampleRate * 3 / 100
		padOut    = SampleRate * 6 / 100
		fade      = SampleRate / 200 // 5 ms
	)
	loud := func(i int) bool {
		var sum float64
		end := min(i+win, len(s))
		for _, v := range s[i:end] {
			f := float64(v) / 32768
			sum += f * f
		}
		return math.Sqrt(sum/float64(max(end-i, 1))) > threshold
	}
	first, last := -1, -1
	for i := 0; i < len(s); i += win {
		if loud(i) {
			if first < 0 {
				first = i
			}
			last = min(i+win, len(s))
		}
	}
	if first < 0 {
		return nil
	}
	start, end := max(first-padIn, 0), min(last+padOut, len(s))
	out := append([]int16(nil), s[start:end]...)
	for i := 0; i < fade && i < len(out); i++ {
		g := float64(i) / fade
		out[i] = int16(float64(out[i]) * g)
		out[len(out)-1-i] = int16(float64(out[len(out)-1-i]) * g)
	}
	return out
}

func writeWAV(path string, pcm []int16) error {
	var b bytes.Buffer
	n := uint32(len(pcm) * 2)
	b.WriteString("RIFF")
	binary.Write(&b, binary.LittleEndian, 36+n)
	b.WriteString("WAVEfmt ")
	binary.Write(&b, binary.LittleEndian, uint32(16))
	binary.Write(&b, binary.LittleEndian, uint16(1)) // PCM
	binary.Write(&b, binary.LittleEndian, uint16(1)) // mono
	binary.Write(&b, binary.LittleEndian, uint32(SampleRate))
	binary.Write(&b, binary.LittleEndian, uint32(SampleRate*2))
	binary.Write(&b, binary.LittleEndian, uint16(2))
	binary.Write(&b, binary.LittleEndian, uint16(16))
	b.WriteString("data")
	binary.Write(&b, binary.LittleEndian, n)
	binary.Write(&b, binary.LittleEndian, pcm)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b.Bytes(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func seconds(samples int) float64 { return float64(samples) / SampleRate }

func round(f float64) float64 { return math.Round(f*1000) / 1000 }

// EstimateSeconds guesses how long a line takes to say at speed, for plan
// checks before any audio exists (≈2.6 words/s at 1.0, plus pauses).
func EstimateSeconds(line string, speed float64) float64 {
	if speed <= 0 {
		speed = 1
	}
	pieces, err := Parse(line)
	if err != nil {
		return float64(len(strings.Fields(Spoken(line)))) / (2.6 * speed)
	}
	t, prev := 0.0, ""
	for _, p := range pieces {
		switch p.Kind {
		case "pause":
			t += p.Pause
		case "text":
			if prev != "" {
				t += naturalGap(prev)
			}
			t += float64(len(strings.Fields(p.Text))) / (2.6 * speed)
			prev = p.Text
		}
	}
	return t
}
