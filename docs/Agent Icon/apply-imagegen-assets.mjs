import fs from 'node:fs/promises';
import path from 'node:path';

const CANVAS = 64;
const HAT_IDS = [
  'baseball-cap', 'beanie', 'beret', 'bucket-hat', 'fedora',
  'bowler', 'newsboy-cap', 'cowboy-hat', 'sailor-cap', 'sun-hat',
  'top-hat', 'crown', 'party-hat', 'wizard-hat', 'flat-cap'
];
const HAIR_IDS = [
  'side-swept', 'quiff', 'mohawk', 'twin-tufts', 'single-curl',
  'slick-back', 'messy-spikes', 'bowl-cut', 'center-part', 'wave',
  'top-knot', 'pompadour', 'afro-puff', 'lightning-fringe', 'short-crop'
];
const EYE_IDS = ['running', 'problem', 'success', 'failure', 'cleanup'];
const TOOL_IDS = [
  'code', 'terminal', 'review', 'debug', 'test', 'research',
  'browser', 'writing', 'docs', 'plan', 'data', 'database',
  'design', 'image', 'chat', 'deploy', 'build', 'git',
  'files', 'automation', 'security', 'config', 'monitor', 'general'
];

function assertOrder(items, expected, label) {
  const actual = items.map(function (item) { return item.id; });
  if (JSON.stringify(actual) !== JSON.stringify(expected)) {
    throw new Error(label + ' order no longer matches the ImageGen atlas: ' + actual.join(', '));
  }
}

function isCheckerboardPixel(r, g, b) {
  const min = Math.min(r, g, b);
  const max = Math.max(r, g, b);
  return min >= 210 && max - min <= 22;
}

// ImageGen occasionally bakes its transparency checkerboard into RGB output.
// Flood only from the sheet boundary, so enclosed white artwork (for example
// the sailor cap) remains intact while the connected checkerboard is removed.
function removeCheckerboard(data, width, height) {
  const pixels = width * height;
  const visited = new Uint8Array(pixels);
  const queue = new Int32Array(pixels);
  let head = 0;
  let tail = 0;

  function enqueue(index) {
    if (visited[index]) return;
    const offset = index * 4;
    if (!isCheckerboardPixel(data[offset], data[offset + 1], data[offset + 2])) return;
    visited[index] = 1;
    queue[tail++] = index;
  }

  for (let x = 0; x < width; x++) {
    enqueue(x);
    enqueue((height - 1) * width + x);
  }
  for (let y = 0; y < height; y++) {
    enqueue(y * width);
    enqueue(y * width + width - 1);
  }

  while (head < tail) {
    const index = queue[head++];
    const x = index % width;
    const y = Math.floor(index / width);
    if (x > 0) enqueue(index - 1);
    if (x + 1 < width) enqueue(index + 1);
    if (y > 0) enqueue(index - width);
    if (y + 1 < height) enqueue(index + width);
  }

  for (let index = 0; index < pixels; index++) {
    if (visited[index]) data[index * 4 + 3] = 0;
  }
  return data;
}

async function readRaster(sharp, file, removeBackground) {
  const result = await sharp(file).ensureAlpha().raw().toBuffer({ resolveWithObject: true });
  const data = Buffer.from(result.data);
  if (removeBackground) removeCheckerboard(data, result.info.width, result.info.height);
  return { data, width: result.info.width, height: result.info.height };
}

function cropRaw(source, left, top, width, height) {
  const data = Buffer.alloc(width * height * 4);
  for (let y = 0; y < height; y++) {
    const sourceStart = ((top + y) * source.width + left) * 4;
    const targetStart = y * width * 4;
    source.data.copy(data, targetStart, sourceStart, sourceStart + width * 4);
  }
  return { data, width, height };
}

function alphaBounds(raw) {
  let left = raw.width;
  let top = raw.height;
  let right = -1;
  let bottom = -1;
  for (let y = 0; y < raw.height; y++) {
    for (let x = 0; x < raw.width; x++) {
      if (raw.data[(y * raw.width + x) * 4 + 3] <= 2) continue;
      left = Math.min(left, x);
      top = Math.min(top, y);
      right = Math.max(right, x);
      bottom = Math.max(bottom, y);
    }
  }
  if (right < left || bottom < top) throw new Error('ImageGen atlas cell contains no visible pixels');
  return { left, top, width: right - left + 1, height: bottom - top + 1 };
}

async function fitLayer(sharp, raw, maxWidth, maxHeight, bottom, right = null) {
  const bounds = alphaBounds(raw);
  const trimmed = cropRaw(raw, bounds.left, bounds.top, bounds.width, bounds.height);
  const resized = await sharp(trimmed.data, { raw: { width: trimmed.width, height: trimmed.height, channels: 4 } })
    .resize({ width: maxWidth, height: maxHeight, fit: 'inside', kernel: sharp.kernel.lanczos3 })
    .png()
    .toBuffer({ resolveWithObject: true });
  const left = right === null
    ? Math.round((CANVAS - resized.info.width) / 2)
    : Math.max(0, right - resized.info.width);
  const top = Math.max(0, bottom - resized.info.height);
  return sharp({ create: { width: CANVAS, height: CANVAS, channels: 4, background: { r: 0, g: 0, b: 0, alpha: 0 } } })
    .composite([{ input: resized.data, left, top }])
    .png()
    .toBuffer();
}

async function writeAtlasLayers(sharp, source, columns, rows, ids, outputDir, fit) {
  await fs.mkdir(outputDir, { recursive: true });
  for (let row = 0; row < rows; row++) {
    const top = Math.round(row * source.height / rows);
    const bottom = Math.round((row + 1) * source.height / rows);
    for (let column = 0; column < columns; column++) {
      const left = Math.round(column * source.width / columns);
      const right = Math.round((column + 1) * source.width / columns);
      const index = row * columns + column;
      const cell = cropRaw(source, left, top, right - left, bottom - top);
      const png = await fitLayer(sharp, cell, fit.width, fit.height, fit.bottom, fit.right ?? null);
      await fs.writeFile(path.join(outputDir, ids[index] + '.png'), png);
    }
  }
}

function parseHex(hex) {
  return [1, 3, 5].map(function (index) { return Number.parseInt(hex.slice(index, index + 2), 16); });
}

async function recolorFrame(sharp, neutralPng, color) {
  const result = await sharp(neutralPng).ensureAlpha().raw().toBuffer({ resolveWithObject: true });
  const data = Buffer.from(result.data);
  const target = parseHex(color);
  for (let offset = 0; offset < data.length; offset += 4) {
    if (data[offset + 3] === 0) continue;
    const r = data[offset];
    const g = data[offset + 1];
    const b = data[offset + 2];
    const max = Math.max(r, g, b);
    const min = Math.min(r, g, b);
    const luminance = (0.2126 * r + 0.7152 * g + 0.0722 * b) / 255;
    // Preserve black outlines, the face screen and its dim LED grid.
    if (luminance < 0.34 || max - min > 38) continue;
    const shade = 0.34 + 0.66 * luminance;
    const specular = Math.max(0, (luminance - 0.78) / 0.22) * 0.7;
    for (let channel = 0; channel < 3; channel++) {
      const tinted = target[channel] * shade;
      data[offset + channel] = Math.round(tinted * (1 - specular) + 255 * specular);
    }
  }
  return sharp(data, { raw: { width: result.info.width, height: result.info.height, channels: 4 } }).png().toBuffer();
}

async function writeEyeLayers(sharp, source, statuses, pngRoot, spriteRoot) {
  const outputFrames = new Map();
  const opacityByStatus = {
    running: [0.76, 0.88, 1, 0.92, 0.82, 0.9],
    problem: [0.62, 0.78, 0.94, 1, 0.86, 0.72],
    success: [0.58, 0.7, 0.82, 0.9, 0.96, 1],
    failure: [0.56, 0.68, 0.8, 0.9, 0.96, 1],
    cleanup: [0.62, 0.74, 0.88, 1, 0.84, 0.7]
  };
  for (let column = 0; column < EYE_IDS.length; column++) {
    const status = statuses[column];
    const frames = [];
    const left = Math.round(column * source.width / EYE_IDS.length);
    const right = Math.round((column + 1) * source.width / EYE_IDS.length);
    const cell = cropRaw(source, left, 0, right - left, source.height);
    const base = await fitLayer(sharp, cell, 32, 14, 45);
    const frameDir = path.join(pngRoot, 'eyes', status.id);
    await fs.mkdir(frameDir, { recursive: true });
    for (let frameIndex = 0; frameIndex < 6; frameIndex++) {
      const frame = await sharp(base)
        .ensureAlpha()
        .composite([{ input: Buffer.from([255, 255, 255, Math.round(opacityByStatus[status.id][frameIndex] * 255)]), raw: { width: 1, height: 1, channels: 4 }, tile: true, blend: 'dest-in' }])
        .png()
        .toBuffer();
      frames.push(frame);
      await fs.writeFile(path.join(frameDir, 'frame-' + String(frameIndex).padStart(2, '0') + '.png'), frame);
    }
    outputFrames.set(status.id, frames);
    await fs.mkdir(path.join(spriteRoot, 'eyes'), { recursive: true });
    await sharp({ create: { width: CANVAS * 6, height: CANVAS, channels: 4, background: { r: 0, g: 0, b: 0, alpha: 0 } } })
      .composite(frames.map(function (input, index) { return { input, left: CANVAS * index, top: 0 }; }))
      .png()
      .toFile(path.join(spriteRoot, 'eyes', status.id + '.png'));
  }
  return outputFrames;
}

async function writeRasterPreview(sharp, pngRoot, previewRoot) {
  const samples = [
    ['violet', 'hats', 'baseball-cap', 'running', 'code'],
    ['cobalt', 'hats', 'beanie', 'problem', 'terminal'],
    ['teal', 'hats', 'bucket-hat', 'success', 'general'],
    ['lime', 'hair', 'lightning-fringe', 'failure', 'debug'],
    ['orange', 'hats', 'top-hat', 'cleanup', 'plan']
  ];
  const iconSize = 58;
  const gap = 10;
  const padding = 12;
  const icons = [];
  for (const [frame, kind, headwear, eyes, tool] of samples) {
    const layered = await sharp({ create: { width: CANVAS, height: CANVAS, channels: 4, background: { r: 0, g: 0, b: 0, alpha: 0 } } })
      .composite([
        { input: path.join(pngRoot, 'frames', frame + '.png'), left: 0, top: 0 },
        { input: path.join(pngRoot, kind, headwear + '.png'), left: 0, top: 0 },
        { input: path.join(pngRoot, 'eyes', eyes, 'frame-05.png'), left: 0, top: 0 },
        { input: path.join(pngRoot, 'tools', tool + '.png'), left: 0, top: 0 }
      ])
      .png()
      .toBuffer();
    const composite = await sharp(layered)
      .resize(iconSize, iconSize, { kernel: sharp.kernel.lanczos3 })
      .png()
      .toBuffer();
    icons.push(composite);
  }
  await fs.mkdir(previewRoot, { recursive: true });
  await sharp({
    create: {
      width: padding * 2 + icons.length * iconSize + (icons.length - 1) * gap,
      height: iconSize + padding * 2,
      channels: 4,
      background: { r: 14, g: 18, b: 25, alpha: 1 }
    }
  })
    .composite(icons.map(function (input, index) {
      return { input, left: padding + index * (iconSize + gap), top: padding };
    }))
    .png()
    .toFile(path.join(previewRoot, 'imagegen-core-combinations.png'));
}

export async function applyImagegenAssets(options) {
  const { sharp, sourceRoot, pngRoot, spriteRoot, previewRoot, hats, hair, frameColors, statuses } = options;
  assertOrder(hats, HAT_IDS, 'hat');
  assertOrder(hair, HAIR_IDS, 'hair');
  assertOrder(statuses, EYE_IDS, 'eye status');

  const [frameSource, hatSource, hairSource, eyeSource, toolSource] = await Promise.all([
    readRaster(sharp, path.join(sourceRoot, 'robot-frame-master-v2.png'), false),
    readRaster(sharp, path.join(sourceRoot, 'headwear-atlas-v3.png'), true),
    readRaster(sharp, path.join(sourceRoot, 'hair-atlas-v3.png'), true),
    readRaster(sharp, path.join(sourceRoot, 'eyes-states-v3.png'), true),
    readRaster(sharp, path.join(sourceRoot, 'tools-atlas-v2.png'), true)
  ]);

  await writeAtlasLayers(sharp, hatSource, 5, 3, HAT_IDS, path.join(pngRoot, 'hats'), { width: 50, height: 27, bottom: 28 });
  await writeAtlasLayers(sharp, hairSource, 5, 3, HAIR_IDS, path.join(pngRoot, 'hair'), { width: 52, height: 30, bottom: 31 });
  await writeAtlasLayers(sharp, toolSource, 6, 4, TOOL_IDS, path.join(pngRoot, 'tools'), { width: 22, height: 22, bottom: 63, right: 63 });

  const neutralFrame = await fitLayer(sharp, frameSource, 62, 48, 64);
  await fs.mkdir(path.join(pngRoot, 'frames'), { recursive: true });
  for (const frame of frameColors) {
    await fs.writeFile(path.join(pngRoot, 'frames', frame.id + '.png'), await recolorFrame(sharp, neutralFrame, frame.color));
  }

  const eyes = await writeEyeLayers(sharp, eyeSource, statuses, pngRoot, spriteRoot);
  await fs.writeFile(path.join(pngRoot, 'templates', 'neutral-led-grid.png'), eyes.get('cleanup')[0]);
  await writeRasterPreview(sharp, pngRoot, previewRoot);
}
