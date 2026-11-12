# S22 Agent markdown and activity

Agent replies render as sanitized markdown. The red "Replying" stamp is gone. A six-dot activity icon in the theme accent sits with a short label. User messages stay plain text. A failed reply uses a neutral card.

No schema change. Versions 19 and 20 are untouched. No runtime image. The guest API is unchanged.

## What the tests show

`TestMarkdownFeatures` covers headings through h4, bold, italic, strike, nested lists, task glyphs, inline code, a fenced block, a table, a quote, a link, a rule, and an image as the text "Image: alt text" with no `img` tag.

`TestMarkdownXSS`, `TestMarkdownHTMLInCode`, `TestFenceInfoIsSafe`, and `TestMarkdownLinkSafety` cover the script, `onerror`, `javascript:`, `data:`, and `vbscript:` set. HTML inside a fence or inline code stays text. `http`, `https`, and `mailto` keep `target="_blank"` and `rel="noopener noreferrer nofollow"`. `TestMarkdownNoInlineScriptOrStyle` rejects inline script and style in the rendered reply.

`TestStreamHalfBoldDoesNotFlip`, `TestStreamHalfLink`, `TestStreamOpenFenceWriting`, `TestStreamFenceCloseBecomesCopy`, `TestStreamTableWaitsForDelim`, `TestStreamListHalfItem`, `TestStreamSplitClosedOnce`, and `TestStreamResumeNoDuplicateBlocks` cover the closed-block and open-tail split. An unclosed fence says "Writing". Closing it becomes Copy. A resume does not emit a closed block twice. The existing `text` SSE events stay, so the S02 stream tests still match incremental chunks.

`TestUserPlain` and `TestUserPlainAndAgentMarkdown` keep `**`, `-`, `#`, and `GITHUB_TOKEN` literal in a user message and link only a bare https URL. The agent body of the same turn is markdown.

`TestActivityLabels` maps Thinking, Reading files, Running command, Waiting for approval, Waking up, Reconnecting, and Working. An unknown tool is Working. Streaming text with no tool is Working and hides the thread row. The mapper never returns Writing or Replying. `TestElapsedAndDwell` shows `14s` and `1m 05s` from 5 seconds and holds a label for 600 ms. `TestRedStampGone` requires the `.act` row and rejects the words Replying and Waking in the pending template. `TestReducedMotionCSS` checks the static frame: animation none, dots 1, 2, and 3 at full opacity, held dots at `--act-held`, caret not blinking. `TestCalmErrorCopy` checks the 15e, 15e2, and 15e3 titles, the kept partial line, and Retry posting `/runs/{id}/retry`. `TestOpenRunQueryScopesUser` shows one user's open run and bash payload to that user only.

## Live

No live tag was run. No machine API was called. Machines created: 0. Spend: $0.

## Evidence

Headless Chrome, dark and light, 1280 and 390, no sideways scroll, no console or CSP errors. Phone tap targets on message actions, code Copy, calm actions, and Try now are at least 44 px. Inline links are exempt. Demo data only. Alex Rivera, workspace Personal, agents Atlas and Wren.

- `15a-dark-1280.png`, `15a-dark-390.png`, `15a-light-1280.png`, `15a-light-390.png` finished markdown reply
- `15b-dark-1280.png`, `15b-dark-390.png`, `15b-light-1280.png`, `15b-light-390.png` streaming list and caret, sidebar Working
- `15b2-dark-1280.png`, `15b2-dark-390.png`, `15b2-light-1280.png`, `15b2-light-390.png` unclosed fence, Writing, dashed edge
- `15d-dark-1280.png`, `15d-dark-390.png`, `15d-light-1280.png`, `15d-light-390.png` Thinking, elapsed 14s
- `15d-still-dark-1280.png`, `15d-still-dark-390.png`, `15d-still-light-1280.png`, `15d-still-light-390.png` same state with reduced motion
- `15d2-dark-1280.png`, `15d2-dark-390.png`, `15d2-light-1280.png`, `15d2-light-390.png` Running command
- `15d3-dark-1280.png`, `15d3-dark-390.png`, `15d3-light-1280.png`, `15d3-light-390.png` Waiting for approval, sidebar needs you
- `15d4-dark-1280.png`, `15d4-dark-390.png`, `15d4-light-1280.png`, `15d4-light-390.png` Waking up, header chip Waking with a static pip
- `15e-dark-1280.png`, `15e-dark-390.png`, `15e-light-1280.png`, `15e-light-390.png` reply stopped, partial text kept
- `15e2-dark-1280.png`, `15e2-dark-390.png`, `15e2-light-1280.png`, `15e2-light-390.png` connection lost and Reconnecting
- `15e3-dark-1280.png`, `15e3-dark-390.png`, `15e3-light-1280.png`, `15e3-light-390.png` machine did not wake
- `15f-dark-1280.png`, `15f-dark-390.png`, `15f-light-1280.png`, `15f-light-390.png` user message stays plain text
