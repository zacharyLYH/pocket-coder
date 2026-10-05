---
name: clear-technical-writing
description: Write explanations, plans, documentation, and technical output in a clear, controlled style inspired by ASD-STE100. Use roughly 80% of the strictness of STE100: maximize clarity and reduce ambiguity without making the prose unnatural.
---

# Clear Technical Writing

Write for fast human comprehension.

Use approximately 80% of the strictness of ASD-STE100. Do not imitate aerospace documentation literally. Preserve natural language when stricter wording would make the output harder to read.

## Core rules

### 1. One idea per sentence

Prefer short sentences.

Bad:
> The service, which is responsible for processing incoming requests and which also performs validation before forwarding them to the worker, can fail when the worker is unavailable.

Good:
> The service processes incoming requests. It validates each request. It then sends the request to the worker. The service can fail when the worker is unavailable.

### 2. Prefer concrete words

Use simple, direct words.

Prefer:
- use → utilize
- start → initiate
- help → facilitate
- show → demonstrate
- change → modify
- before → prior to
- because → due to the fact that
- if → in the event that

Avoid unnecessary jargon and abstract nouns.

### 3. Prefer active voice

Prefer:

> The server stores the session.

Over:

> The session is stored by the server.

Use passive voice only when the actor is unknown, irrelevant, or obvious.

### 4. Make relationships explicit

Do not force the reader to infer cause, condition, or sequence.

Weak:
> The request fails when authentication is enabled.

Better:
> When authentication is enabled, the request fails because the token is missing.

### 5. Avoid ambiguous references

Do not use vague words such as:

- this
- that
- it
- they
- these
- those

when the referenced object could be unclear.

Weak:
> The worker sends the result to the API. This can fail.

Better:
> The worker sends the result to the API. The API request can fail.

### 6. Prefer positive instructions

Prefer:

> Set `timeout` to 30 seconds.

Over:

> Do not leave `timeout` unset.

Use negative instructions when they communicate an important prohibition.

### 7. Keep terminology consistent

Use one term for one concept.

Do not alternate between:

> request / query / call / operation

unless they actually mean different things.

If a technical term has a specific meaning, keep that meaning throughout the explanation.

### 8. Remove unnecessary qualifiers

Avoid words that add little information:

- basically
- generally
- typically
- obviously
- simply
- quite
- very
- actually
- essentially
- probably

Keep them when they communicate real uncertainty or scope.

### 9. Prefer direct structure

For technical explanations, use this order when appropriate:

1. What it is
2. Why it exists
3. How it works
4. Example
5. Important limitations

Do not bury the main point.

### 10. Use examples aggressively

When a concept is abstract, give a concrete example.

Prefer:

> A cache stores data so the next request can avoid doing the same expensive work.

Over:

> A cache provides a mechanism for optimizing repeated data access.

### 11. Keep paragraphs small

One paragraph should normally communicate one main idea.

Use headings and lists when they reduce cognitive load.

Do not turn every sentence into a bullet point.

### 12. Avoid unnecessary repetition

State an idea once, clearly.

Do not restate the same conclusion in several different ways unless repetition improves comprehension.

## Technical explanations

When explaining technical concepts:

- Start with the simplest accurate mental model.
- Introduce terminology after the concept is understood.
- Use concrete examples before formal definitions when possible.
- Explain cause and effect explicitly.
- Separate facts from assumptions.
- State important edge cases.
- Do not add implementation details that do not help the reader understand the current concept.

## Code explanations

When discussing code:

- Refer to exact symbols, functions, files, or values.
- Explain what the code does before explaining why it does it.
- Use small examples.
- Do not describe obvious syntax unless it matters.
- If behavior depends on an assumption, state the assumption.

## Decision-making

When presenting options:

- State the recommendation first.
- Give the main reason.
- List important trade-offs.
- Avoid presenting five equivalent options when one is clearly better.

Example:

> **Recommendation:** Use PostgreSQL.
>
> It is the simplest option for this workload.
>
> The main trade-off is that you lose some of the flexibility of a document database.

## What NOT to do

Do not make the writing sound like a manual written by a machine.

Avoid excessive STE-style restrictions such as:

> The user must perform the following action.

when this is clearer:

> Run the command below.

Do not remove natural conversational language merely to satisfy a formal rule.

Do not sacrifice precision for simplicity.

## Final pass

Before producing the answer, check:

- Can each sentence be understood on the first reading?
- Does each pronoun have a clear referent?
- Did I use the same term for the same concept?
- Did I remove unnecessary words?
- Did I make cause and effect explicit?
- Did I separate different ideas?
- Is the answer still natural to read?
- Did I preserve technical precision?

The goal is **clear, precise, compact, natural language**.

Think:

> "80% ASD-STE100, 100% understandable."

Not:

> "Write like an aerospace maintenance manual."