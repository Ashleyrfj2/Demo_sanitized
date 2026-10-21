#!/usr/bin/env node
// Dependency-free, conservative pre-public scan of the *tracked working tree*.
// This is a guardrail, not a full history scan and not proof that a repo is safe to publish.
import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";

const tracked = execFileSync("git", ["ls-files", "-z"], { encoding: "utf8" })
  .split("\0").filter(Boolean);
const findings = [];
const unsafePaths = [
  /(^|\/)\.env(?:\.(?!example$)[^/]+)?$/,
  /(^|\/)config\.local\.json$/,
  /(^|\/)docker-compose\.override\.yml$/,
  /(^|\/)\.generated\//,
  /\.(?:pem|p12|pfx|key)$/i,
];
const secretPatterns = [
  ["private-key block", /-----BEGIN (?:RSA |EC |OPENSSH |DSA )?PRIVATE KEY-----/g],
  ["GitHub token", /\b(?:gh[pousr]_[A-Za-z0-9_]{30,}|github_pat_[A-Za-z0-9_]{35,})\b/g],
  ["AWS access key", /\b(?:AKIA|ASIA)[A-Z0-9]{16}\b/g],
  ["OpenAI-style key", /\bsk-(?!example|replace|test)[A-Za-z0-9_-]{32,}\b/g],
  ["Slack token", /\bxox[baprs]-[A-Za-z0-9-]{20,}\b/g],
  ["JWT", /\beyJ[A-Za-z0-9_-]{15,}\.eyJ[A-Za-z0-9_-]{15,}\.[A-Za-z0-9_-]{5,}\b/g],
];
for (const path of tracked) {
  if (unsafePaths.some((pattern) => pattern.test(path))) {
    findings.push({ path, rule: "sensitive file path" });
    continue;
  }
  let content;
  try { content = readFileSync(path, "utf8"); }
  catch { continue; }
  if (content.includes("\0")) continue;
  if (path.startsWith("frontend/") && /import\.meta\.env\.VITE_[A-Z0-9_]*(?:TOKEN|SECRET|PASSWORD|PRIVATE_KEY|API_KEY)/.test(content)) {
    findings.push({ path, rule: "client-build environment variable used as a credential" });
  }
  for (const [rule, pattern] of secretPatterns) {
    pattern.lastIndex = 0;
    if (pattern.test(content)) findings.push({ path, rule });
  }
  if (path.endsWith(".md")) {
    const match = content.match(/^\s*Password:\s*(\S[^\n]*)$/im);
    if (match && !/^(?:<[^>]+>|\*+|\[redacted\]|replace|placeholder|example)/i.test(match[1].trim())) {
      findings.push({ path, rule: "documented plaintext password" });
    }
  }
}
if (findings.length) {
  console.error("Public-release credential scan found possible exposure:");
  for (const { path, rule } of findings) console.error(`- ${path}: ${rule}`);
  console.error("Secret values are intentionally not printed. Rotate any real exposed credentials.");
  process.exitCode = 1;
} else {
  console.log(`No high-confidence secrets found in ${tracked.length} tracked files (current tree only).`);
}
