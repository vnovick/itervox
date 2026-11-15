// Intrinsic size of a PNG or WebP file in public/, read from its header at
// build time, so a figure needs no hand-kept size registry.
import { readFileSync } from 'node:fs';
import { join } from 'node:path';

export function publicImageSize(publicPath: string): [number, number] {
  const file = join(process.cwd(), 'public', publicPath);
  let b: Buffer;
  try {
    b = readFileSync(file);
  } catch {
    throw new Error(`GuideShot: ${publicPath} not found in site/public (see web/e2e/guide-shots/README.md)`);
  }
  // PNG: IHDR width/height, big-endian, at bytes 16 and 20.
  if (b.readUInt32BE(0) === 0x89504e47) return [b.readUInt32BE(16), b.readUInt32BE(20)];
  if (b.toString('ascii', 0, 4) === 'RIFF' && b.toString('ascii', 8, 12) === 'WEBP') {
    const chunk = b.toString('ascii', 12, 16);
    if (chunk === 'VP8X') return [1 + b.readUIntLE(24, 3), 1 + b.readUIntLE(27, 3)];
    if (chunk === 'VP8 ') return [b.readUInt16LE(26) & 0x3fff, b.readUInt16LE(28) & 0x3fff];
    if (chunk === 'VP8L') {
      const bits = b.readUInt32LE(21);
      return [1 + (bits & 0x3fff), 1 + ((bits >> 14) & 0x3fff)];
    }
  }
  throw new Error(`GuideShot: ${publicPath} is not a PNG or WebP image`);
}
