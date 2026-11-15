import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { describe, expect, it } from 'vitest';
import {
  ProfileBackendsSchema,
  PromptRefCheckSchema,
  PromptRefRowSchema,
  PromptSchema,
} from '../schemas';

// #123: the Zod schemas for the backend-aware skills routes mirror the Go
// DTOs field for field (vitest runs with cwd = web/).

function goStruct(file: string, name: string): string {
  const goPath = resolve(process.cwd(), `../${file}`);
  const src: string = readFileSync(goPath, 'utf8');
  const m = new RegExp(`type ${name} struct \\{([\\s\\S]*?)\\n\\}`).exec(src);
  if (!m) throw new Error(`struct ${name} not found in ${file}`);
  return m[1];
}

/** JSON names of a struct's fields (json tag, or the Go name); "-" skipped. */
function jsonFields(body: string): string[] {
  const out: string[] = [];
  for (const line of body.split('\n')) {
    const m = /^\s*([A-Z]\w*)\s+[^`/]+?(?:`json:"([^",]*)[^`]*`)?\s*(?:\/\/.*)?$/.exec(line);
    if (!m) continue;
    const tag = m[2] as string | undefined; // unset when the field has no json tag
    const name = tag ?? m[1];
    if (name !== '-') out.push(name);
  }
  return out.sort();
}

const zodKeys = (schema: { shape: Record<string, unknown> }) => Object.keys(schema.shape).sort();

describe('skills reference-check schema parity (#123)', () => {
  it('PromptRefRowSchema matches promptRefRow', () => {
    expect(zodKeys(PromptRefRowSchema)).toEqual(
      jsonFields(goStruct('internal/server/skills_refs.go', 'promptRefRow')),
    );
  });

  it('PromptRefCheckSchema matches promptRefCheckResponse', () => {
    expect(zodKeys(PromptRefCheckSchema)).toEqual(
      jsonFields(goStruct('internal/server/skills_refs.go', 'promptRefCheckResponse')),
    );
  });

  it('PromptSchema matches skills.Prompt', () => {
    expect(zodKeys(PromptSchema)).toEqual(
      jsonFields(goStruct('internal/skills/types.go', 'Prompt')),
    );
  });

  it('ProfileBackendsSchema matches the profile-backends response keys', () => {
    const src = readFileSync(resolve(process.cwd(), '../internal/server/skills_refs.go'), 'utf8');
    expect(src).toContain('map[string]any{"default": def, "profiles": profiles}');
    expect(zodKeys(ProfileBackendsSchema)).toEqual(['default', 'profiles']);
  });
});
