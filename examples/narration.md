# Writing voiceover lines

Each scene in a plan can have a `narration` line. The `voiceover` tool speaks it with the
voice engine picked in Settings → Voice (Kokoro by default: local, free, no login) and places
it 0.25 s into its scene.

| Markup | Effect |
|---|---|
| `Meet Biscuit.` | A full stop leaves about 0.4 s before the next sentence; a comma about 0.2 s. |
| `[pause]` | Exactly 0.5 s of silence. |
| `[pause 0.8]` | Exactly 0.8 s of silence (up to 3 s). |
| `{match}` | A cue: marks the word that follows. The tool returns its film time so the click or reveal can land on it. |

## Examples

```
Somewhere, a horse is lonely.
Meet Biscuit. [pause 0.3] Seven, enjoys long gallops.
One swipe, {match} and it's a match.
[pause 0.8] Horse Tinder.
```

What `voiceover` reports for a run (abridged):

```
line          scene window    voice                    room before cut
hook           0.00– 3.00s     0.25– 2.05s (1.80s)   +0.95s
demo           7.00–12.00s     7.25–10.10s (2.85s)   +1.90s

Cues: land the matching visual here. The action may lead the word by up to 0.15s, never trail it:
  {match} @ 8.62s  (line demo)
```

## Rules of thumb

- Say what's on screen as it appears; never describe a screen that's gone or not yet shown.
- About 2.5 words a second: a 4-second scene holds about 8 words. `submit_plan` refuses a line
  that can't fit its scene.
- The caption is the headline, the voice is the sentence around it. Don't read captions aloud.
- Put a pause before a result or reveal so the picture lands in silence.
- Fix a line that runs over by lengthening the scene, not by speeding up the voice.
