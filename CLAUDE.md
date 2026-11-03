# Ella SMSC - AI Coding Agent Instructions

## Code Comments

**Write zero comments.** Do not add explanatory comments, section headers, TODOs, doc comments, field comments, or inline comments to any Go or UI code you write or modify, not even for non-obvious logic.

The only allowed comments are:

- **Spec citations**, written bare, one per line, directly above a function, method, type, or const/var declaration. No prose, no trailing period:

  ```go
  // TS 23.040 clause 9.2.3.9
  // SMPP 3.4 section 4.4.1
  // RFC 6733 section 5.3
  // OpenID Connect Core 1.0 section 3.1.3.7
  ```

  3GPP specs use `clause`; SMPP, RFCs, and OpenID specs use `section`. Never put a citation inside a function body or on a struct field. Only cite a clause that you have confirmed in the spec (3GPP specs are in `../3gpp`). Never guess a clause number.
- **Comments that tooling requires**: `//go:build`, `//go:embed`, `//go:generate`, `//nolint:<linter>` (with no trailing reason), `// eslint-disable…`, `// @ts-expect-error`.

**Do not touch existing citations** unless your change makes one wrong. In that case, fix it or delete it.
