# Demo — Public Source Snapshot

A source snapshot of a local-only validation-evidence demo. This repository starts with a **new Git history** and intentionally excludes the private Demo repository's commits, agent logs, internal handoffs, recordings, and operational receipts.

## Safety model

- **Local development only.** Backend and browser extension integrations are bound to loopback hosts; do not expose the API on the internet.
- **No secrets in Git.** Generate local credentials, keep them in ignored files, and never place API tokens in `VITE_*` variables or frontend bundles.
- Browser-entered demo tokens remain in memory for the current page and are not appropriate for production authentication.
- Test fixture names and actors are synthetic. No operational customer or tester data is intended to be present.
- This is source code, not a hosted service. Publicly committed API routes, protocol definitions, and implementation logic **are public**. Any proprietary server logic must remain in a separate private service.

## Local setup

Requires Go, Node.js, npm, and Docker Compose.

```bash
npm ci --prefix frontend
npm ci --prefix extension
node scripts/demo/create-local-auth.mjs
docker compose up --build -d
npm run dev --prefix frontend
```

The generated local credentials are stored in ignored `.env` and `demo/.generated/source-tokens.json` files. Enter the `human:late-tester-d` token in the frontend prompt. Do not paste tokens into documentation or commits.

## Security checks

```bash
node scripts/check-public-secrets.mjs
go test ./backend/internal/...
npm test --prefix frontend
npm test --prefix extension
```

The scanning script checks the current tracked tree for common secret shapes; it is not a substitute for a full security review. A fresh commit history prevents the original repository's older credential-containing Git objects from being copied.

## Scope

This is a **sanitized demonstration snapshot**, not the authoritative internal repository. Internal collaboration notes, recorded proof, team setup guides, and prior Git history are intentionally omitted. Do not import the old repository's history using a fork, merge, or push-mirror.

No open-source license has been assigned. All rights are reserved unless the owner adds a license.
