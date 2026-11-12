# Prompts

Paste these into ChatGPT (or any MCP client) after connecting Smart Control. Replace the
parts in angle brackets. Start a new chat after you update or restart the server, because
the client keeps the tool list from when the chat started.

## Make a launch video from a project

```
Make a brag video for my project at ~/projects/<project>.

Who it's for: <audience, e.g. small shop owners>
The one thing worth bragging about: <the moment, e.g. one message drafts a purchase order>
Tone: <calm and confident | playful | deadpan>
Format: <landscape | vertical | square>, about 20 seconds.

Call brag_guide with topic "index" first and follow it. Use screenshots only to see how the
app looks and to plan; rebuild the UI from the source code for the video. Show me the plan,
including every voiceover line, and wait for my approval before building.
```

## Make a launch video from a website

```
Make a vertical brag video for https://<site>. Read brag_guide("index") first. The video is for
<audience>; the hook is <the one moment>. Show me the plan and wait for my approval.
```

## Add a voiceover to an existing run

```
Continue brag run <run_id>. Add a voiceover:
1. Read plan.json and composition/index.html, and note each scene's real start and end time.
2. Write one line per scene that says what the viewer sees, as it appears. Short spoken
   sentences; [pause 0.4] before a reveal; a {cue} right before the word each click lands on.
3. Suggest a voice from voiceover list_voices: true.
4. Show me a table (scene, time window, line, estimated length) and wait for my OK.
Then call voiceover, fix every PROBLEM by retiming scenes, move the actions onto the cue times,
check, review every scene with review_scene, render, finalize.
Speech comes only from the voiceover tool: never sign in to HeyGen or use a cloud voice.
```

## Fix a video that looks wrong

```
Brag run <run_id> has problems: <e.g. the text is too small, scenes don't line up, the phone is
cropped>. Call review_scene for every scene, write what you see in review.md under
"## <scene id>", fix each problem, re-check, review the scenes you changed, and render again.
```

## Use real screenshots on purpose

```
For this brag, I want the real app screenshots in the video, not a rebuilt UI.
```

The AI then quotes you in the plan's `screenshots_requested`, which lets screenshots through
the final-render check.

## Draft the LinkedIn post

```
Write a LinkedIn post for brag run <run_id> using its share copy, and save it with
post_linkedin_post with brag.mp4 attached. Don't publish it.
```

You publish from the dashboard (Posts → Post to LinkedIn).
