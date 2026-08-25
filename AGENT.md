# Coding standards
1. MINIMAL DEPENDENCIES. Fewer dependencies and the more mainstream and battle tested the dependencies, the better. 
2. MINIMAL LINES OF CODE. More lines of code === more complexity. That is evil.
3. JUDGEMENT CALLS SHOULD BE SENSIBLE DEFAULTS. Sometimes users don't spell out every minute detail, sensible defaults is your target when behavior / features are ambiguous. One thing you can ask yourself is "How would Github Codespaces a polished Saas app do this behavior if they had to?"
4. DEFER TESTS UNTIL USER HAS CONFIRMED FEATURE PARITY. Otherwise, you might just be wasting user's time creating tests when the feature might not be ready yet!
5. NO THIN MODULES NOR BIG FUNCTIONS. To know when abstraction is a good idea, ask yourself if reading some abstracted function actually reduces mental friction. 
6. ALWAYS USE UNSLOP SKILL!

# Go code
1. INTERFACES AND MOCKS. APIs should be interfaces so that they can be easily mocked using Mockery.
2. TESTS. Unit tests and intg tests are a must. Where it makes sense, use the mockery generated mocks.

# Typescript code
1. TYPED. Everything must be typed. 
2. TESTS. Playwright behavioral tests should be first class from day 1 (visual/screenshot tests deferred for now). Mocks are allowed, but push it down as far as possible - we want to exercise as much behavior as possible!
3. USE SHADCN COMPONENTS AND TAILWIND CSS. Prioritize using fleshed out and polished ShadCN components found here: https://ui.shadcn.com/docs/components
4. TAILWIND CSS RESPONSIVE DESIGN IS MOBILE FIRST. Custom styles are for larger screens, non-custom are assumed to be for mobile screens.
5. MOCK API BEHAVIOR. Do not spin up a real golang server. Just mock the API behavior your test expects! Keeps code decoupled and even allows us to test API failure / edge cases much easier!

# Agent learnings
Anytime you have a tough time doing something and learn of a thing that can help future agents expedite this process, you should document it as a skill. These are some examples of things that should be skills:
1. Sharp edge that requires workarounds 
2. Weird setup requirements for testing 
3. Custom throwaway code you wrote that helps us do debugging in general - not some script that helps us debug only one edge case

Write it into the skills folder. This is how to structure skills inside this folder: 
```
my-skill/
├── SKILL.md              # Required: main instructions
├── references/           # Optional: additional documentation
│   ├── api-guide.md
│   ├── examples.md
│   └── troubleshooting.md
├── scripts/              # Optional: executable code
│   ├── generate.py
│   └── validate.sh
├── templates/            # Optional: file templates
│   ├── component.tsx.template
│   └── test.spec.ts.template
└── assets/               # Optional: static files
    ├── logo.png
    └── config.json
```
```
SKILL.md (Required)
The main skill file containing:

YAML frontmatter with metadata
Markdown instructions for Claude
---
name: my-skill
description: What this skill does and when to use it.
---

# Skill Title

Instructions go here...
```

What DOESN'T go in here are:
1. Details that can be found or easily inferred in code
2. Information about the business