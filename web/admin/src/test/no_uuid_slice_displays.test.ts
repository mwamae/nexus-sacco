// Guardrail: no .slice(0, N) + '…' pattern in user-facing source.
//
// This is the exact shape that caused the "9c4dc64b…" leak that
// inspired the human-label refactor. Use the Ref / Name / Label
// resolver components in src/components/refs/ instead.
//
// Per-file allowlist below for cases where the slicing is NOT a
// display of a UUID (e.g. truncating a long product name). New
// entries require a comment explaining why.

import { readdirSync, readFileSync, statSync } from 'node:fs';
import { join, relative } from 'node:path';
import { describe, expect, it } from 'vitest';

const ROOT = join(__dirname, '..'); // /web/admin/src

// Matches `.slice(0, N) + '…'` for N in 4..16 — the typical "show a
// shortened uuid" pattern. Tighter bounds reduce false positives on
// legitimate text-snippet truncation (anything ≥ 20 chars is almost
// certainly human-readable).
const FORBIDDEN = /\.slice\(\s*0\s*,\s*([4-9]|1[0-6])\s*\)\s*\+\s*['"]…['"]/;

// Files where slicing has a non-display purpose. Each entry needs a
// short justification.
const ALLOWLIST: Record<string, string> = {
  // Product name truncation: `trimmed.slice(0, 14) + '…'` shortens a
  // long human-typed product name to fit a card title. Not a UUID.
  'pages/LoanProducts.tsx':
    'human-name truncation, not uuid display',
};

function walk(dir: string, acc: string[] = []): string[] {
  for (const name of readdirSync(dir)) {
    // Don't crawl into the test harness itself.
    if (name === 'test' || name === 'node_modules') continue;
    const p = join(dir, name);
    const s = statSync(p);
    if (s.isDirectory()) walk(p, acc);
    else if (/\.(tsx?|jsx?)$/.test(name) && !name.endsWith('.test.tsx') && !name.endsWith('.test.ts')) {
      acc.push(p);
    }
  }
  return acc;
}

describe('no .slice(0, N) + "…" pattern in user-facing source', () => {
  it('grep finds zero non-allowlisted hits', () => {
    const files = walk(ROOT);
    const violations: { file: string; line: number; text: string }[] = [];
    for (const path of files) {
      const rel = relative(ROOT, path);
      if (ALLOWLIST[rel]) continue;
      const body = readFileSync(path, 'utf8').split('\n');
      body.forEach((line, idx) => {
        // Strip everything from `//` onwards so JSdoc + line
        // comments that document the old pattern don't count as
        // violations. Block comments aren't stripped (rare and
        // not worth the parser); add to the allowlist if hit.
        const noComment = line.replace(/\/\/.*$/, '');
        if (FORBIDDEN.test(noComment)) {
          violations.push({ file: rel, line: idx + 1, text: line.trim() });
        }
      });
    }
    if (violations.length > 0) {
      const msg = violations
        .map((v) => `  ${v.file}:${v.line}  ${v.text}`)
        .join('\n');
      throw new Error(
        `Found ${violations.length} forbidden uuid-slice display(s).\n` +
          `Replace with a Ref/Name/Label resolver from src/components/refs/, or\n` +
          `add the file to the allowlist in this test with a one-line\n` +
          `justification.\n\n${msg}`,
      );
    }
    expect(violations.length).toBe(0);
  });
});
