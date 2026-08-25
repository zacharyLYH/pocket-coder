# RFC: Copy and paste must work in the web terminal

Status: proposed, not yet scheduled. Written from observed breakage; the
terminal currently offers no reliable way to move text between the user's
clipboard and a running session.

## Summary

The web terminal renders a real CLI, but the text inside it is unreachable by
the usual browser reflexes. Users cannot reliably select and copy command
output, and pasting from their clipboard into the session does not work
dependably. For a platform whose users spend their time shuttling commands,
URLs, tokens, and error messages between notes, chat, and terminals, this is a
daily-quality-of-life gap, not an edge case.

## Why it matters

- **The workflow is copy-shaped.** Side-project development is constant small
  transfers: paste an install command, copy an error into search, copy a URL,
  duplicate a snippet. Every one of these dead-ends today.
- **Mobile removes the fallbacks.** On a desktop you might survive with
  middle-click quirks or terminal menus. On a phone there is no keyboard
  shortcut at all, and the long-press selection UI that works on ordinary web
  text does not appear over the terminal surface.
- **Agent workflows amplify it.** Reviewing and steering coding CLIs means
  pasting prompts, paths, and snippets into the conversation. A paste flow
  that mangles or drops text corrupts the instruction rather than failing loudly.
- **Silent partial failure erodes trust.** A paste that executes line-by-line
  instead of arriving as one block can run destructive commands before the
  user notices. This failure mode is worse than no paste at all.

## Why it is broken (constraints, not causes)

Any fix operates inside these realities of the stack:

1. The terminal draws onto its own surface, so the browser's native
   text-selection machinery does not apply to it.
2. Running programs sometimes ask to receive mouse events themselves; when
   they do, drag-selection collides with the program's own use of the mouse.
3. Browsers guard clipboard access behind permissions and user gestures;
   nothing can be copied or pasted silently or in the background.
4. Multi-line text pasted into a shell is interpreted line-by-line unless the
   program signals otherwise, which turns a paste into execution.
5. The bridge carries keystrokes as opaque data, so anything clipboard-related
   must be deliberate end-to-end rather than inherited for free.

The desired-state section below states what must be true regardless of which
of these constraints each mechanism threads.

## Desired success state

A user on desktop, phone, or tablet can, without instruction:

1. **Select** any visible terminal output, including output produced while a
   full-screen program is running, using whatever gesture is natural on their
   device.
2. **Copy** that selection to the system clipboard with the standard shortcut
   or affordance, and see some acknowledgment that something was copied.
3. **Paste** arbitrary text from the system clipboard into the running
   program, where it arrives exactly as copied: same characters, same order,
   nothing executed early.
4. **Repeat safely**: round-trips work repeatedly in one session and across
   reconnections, including large blocks (a screenful or more).

Non-goals for v1: shared/synced clipboards across devices, copying rich
formatting or colors (plain text only), and clipboard history management.

## Gate

E2E, against a fixture project's real terminal through the websocket bridge:

1. **Echo round-trip (desktop).** Type a marker command, let output render,
   select part of the output with mouse events, invoke copy, and assert the
   system clipboard contains the selected text.
2. **Clipboard to session (desktop).** Place a known multi-word string on the
   system clipboard, invoke paste, and assert the session receives it intact:
   the echoed result matches byte-for-byte.
3. **Multi-line safety.** Paste a multi-line block into a shell that supports
   bracketed input; assert the lines arrive together and are not executed one
   by one mid-paste (observable via prompt behavior or a sentinel file check).
4. **Mouse-reporting conflict.** Run a program that captures mouse events
   (vi-family suffices); assert a documented gesture still selects and copies
   visible text.
5. **Mobile path.** On a phone-profile context: long-press (or the shipped
   affordance) selects output and offers copy; a paste affordance inserts
   clipboard content; both verified by the same assertions as gates 1 and 2.
6. **Persistence.** Repeat gate 1 after killing and reattaching the session;
   copy and paste still work with no page reload tricks.

All gates run in CI alongside the existing terminal specs; visual snapshots
are updated if any affordance changes the chrome around the pane.
