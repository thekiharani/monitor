import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import sharp from "sharp";

const here = dirname(fileURLToPath(import.meta.url));
const publicDir = resolve(here, "../public");
const markSource = readFileSync(resolve(here, "../src/components/layout/MonitorMark.tsx"), "utf8");

function constant(name) {
  const match = markSource.match(new RegExp(`${name} = ([^;]+);`));
  if (!match) throw new Error(`MonitorMark.tsx no longer exports ${name}`);
  return JSON.parse(match[1].replaceAll(/(\w+):/g, '"$1":').replaceAll("'", '"'));
}

const PULSE = constant("MONITOR_MARK_PULSE");
const STROKE = constant("MONITOR_MARK_STROKE");

const TILE_FROM = "#00a6a3";
const TILE_TO = "#007f90";
const GLYPH = "#f5fefe";

function tile({ size, radius, padding, background }) {
  const scale = (size - padding * 2) / 64;
  return `<svg xmlns="http://www.w3.org/2000/svg" width="${size}" height="${size}" viewBox="0 0 ${size} ${size}" role="img" aria-labelledby="mark-title">
  <title id="mark-title">Noria Monitor</title>
  <defs>
    <linearGradient id="tile" x1="0" y1="0" x2="${size}" y2="${size}" gradientUnits="userSpaceOnUse">
      <stop stop-color="${TILE_FROM}"/>
      <stop offset="1" stop-color="${TILE_TO}"/>
    </linearGradient>
  </defs>
  <rect width="${size}" height="${size}" rx="${radius}" fill="${background ?? "url(#tile)"}"/>
  <g transform="translate(${padding} ${padding}) scale(${scale})" fill="none" stroke="${GLYPH}" stroke-width="${STROKE}" stroke-linecap="round" stroke-linejoin="round">
    <path d="${PULSE}"/>
  </g>
</svg>
`;
}

function icoFrom(pngBuffers) {
  const count = pngBuffers.length;
  const header = Buffer.alloc(6);
  header.writeUInt16LE(0, 0);
  header.writeUInt16LE(1, 2);
  header.writeUInt16LE(count, 4);

  let offset = 6 + count * 16;
  const entries = [];
  for (const { size, data } of pngBuffers) {
    const entry = Buffer.alloc(16);
    entry.writeUInt8(size >= 256 ? 0 : size, 0);
    entry.writeUInt8(size >= 256 ? 0 : size, 1);
    entry.writeUInt8(0, 2);
    entry.writeUInt8(0, 3);
    entry.writeUInt16LE(1, 4);
    entry.writeUInt16LE(32, 6);
    entry.writeUInt32LE(data.length, 8);
    entry.writeUInt32LE(offset, 12);
    entries.push(entry);
    offset += data.length;
  }
  return Buffer.concat([header, ...entries, ...pngBuffers.map((p) => p.data)]);
}

mkdirSync(publicDir, { recursive: true });

const faviconSvg = tile({ size: 64, radius: 14, padding: 9 });
writeFileSync(resolve(publicDir, "favicon.svg"), faviconSvg);

const raster = [
  { file: "favicon-16x16.png", size: 16, radius: 4, padding: 2 },
  { file: "favicon-32x32.png", size: 32, radius: 7, padding: 4 },
  { file: "apple-touch-icon.png", size: 180, radius: 40, padding: 30 },
  { file: "icon-192.png", size: 192, radius: 43, padding: 27 },
  { file: "icon-512.png", size: 512, radius: 114, padding: 72 },
  { file: "icon-maskable-512.png", size: 512, radius: 0, padding: 128 },
  { file: "email-logo.png", size: 128, radius: 29, padding: 18 },
];

for (const { file, size, radius, padding } of raster) {
  const svg = tile({ size, radius, padding });
  const png = await sharp(Buffer.from(svg), { density: 384 })
    .resize(size, size)
    .png({ compressionLevel: 9 })
    .toBuffer();
  writeFileSync(resolve(publicDir, file), png);
  console.log(`${file.padEnd(24)} ${size}x${size}`);
}

const icoSizes = [16, 32, 48];
const icoParts = [];
for (const size of icoSizes) {
  const svg = tile({
    size,
    radius: Math.round(size * 0.22),
    padding: Math.round(size * 0.14),
  });
  const data = await sharp(Buffer.from(svg), { density: 384 })
    .resize(size, size)
    .png({ compressionLevel: 9 })
    .toBuffer();
  icoParts.push({ size, data });
}
writeFileSync(resolve(publicDir, "favicon.ico"), icoFrom(icoParts));

console.log(`${"favicon.ico".padEnd(24)} ${icoSizes.join(", ")}`);
console.log(`${"favicon.svg".padEnd(24)} vector`);
