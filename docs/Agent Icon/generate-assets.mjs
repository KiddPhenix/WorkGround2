import fs from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { createRequire } from 'node:module';

const here = path.dirname(fileURLToPath(import.meta.url));
const outRoot = path.join(here, 'assets');
const svgRoot = path.join(outRoot, 'svg');
const pngRoot = path.join(outRoot, 'png');
const spriteRoot = path.join(outRoot, 'sprites');
const previewRoot = path.join(outRoot, 'previews');
const runtimeRoot = process.env.CODEX_NODE_MODULES
  ? path.dirname(process.env.CODEX_NODE_MODULES)
  : path.resolve(here, '..', '..', '..', 'desktop', 'frontend');
const require = createRequire(path.join(runtimeRoot, 'package.json'));
const sharp = require('sharp');

const INK = '#111722';
const FACE = '#111821';
const FACE_EDGE = '#070B11';
const WHITE = '#F5F7FB';
const MUTED_LED = '#29313B';
const VIEWBOX = '0 0 64 64';

const frameColors = [
  { id: 'violet', label: 'Violet', color: '#7B61FF' },
  { id: 'cobalt', label: 'Cobalt', color: '#3478F6' },
  { id: 'cyan', label: 'Cyan', color: '#22B8F0' },
  { id: 'teal', label: 'Teal', color: '#20B8A6' },
  { id: 'green', label: 'Green', color: '#42C96B' },
  { id: 'lime', label: 'Lime', color: '#A7D92D' },
  { id: 'amber', label: 'Amber', color: '#F3B83F' },
  { id: 'orange', label: 'Orange', color: '#F47A32' },
  { id: 'coral', label: 'Coral', color: '#EF5D78' }
];

function painted(fill, body, stroke = INK) {
  return '<g fill="' + fill + '" stroke="' + stroke + '" stroke-width="2.25" stroke-linecap="round" stroke-linejoin="round">' + body + '</g>';
}

const hats = [
  {
    id: 'baseball-cap',
    label: 'Baseball Cap',
    body: painted('#7B61FF', '<path d="M16 20c1-9 7-14 16-14s15 5 16 14H16Z"/><path d="M31 7v13"/><path d="M48 18c6 0 10 2 12 5-6 1-12 1-18-1l6-4Z"/>')
  },
  {
    id: 'beanie',
    label: 'Beanie',
    body: painted('#3478F6', '<circle cx="32" cy="5" r="3.5"/><path d="M16 19c1-10 7-15 16-15s15 5 16 15H16Z"/><path d="M15 18h34v7H15z"/><path d="M21 18v7M28 18v7M36 18v7M43 18v7"/>')
  },
  {
    id: 'beret',
    label: 'Beret',
    body: painted('#20B8A6', '<path d="M13 18c5-10 16-15 29-12 7 2 10 6 9 10-2 5-12 7-24 7-9 0-15-1-14-5Z"/><path d="M31 6l2-4"/>')
  },
  {
    id: 'bucket-hat',
    label: 'Bucket Hat',
    body: painted('#F3B83F', '<path d="M19 7h26l4 15H15L19 7Z"/><path d="M12 21h40l7 5H5l7-5Z"/><path d="M20 12h25"/>')
  },
  {
    id: 'fedora',
    label: 'Fedora',
    body: painted('#8A6A50', '<path d="M20 5h24l4 17H16L20 5Z"/><path d="M16 16h32"/><path d="M8 22h48l4 4H4l4-4Z"/><path d="M27 6l-2 9"/>')
  },
  {
    id: 'bowler',
    label: 'Bowler',
    body: painted('#3C4658', '<path d="M17 19C17 9 23 4 32 4s15 5 15 15H17Z"/><path d="M10 19h44l4 5H6l4-5Z"/><path d="M18 16h28"/>')
  },
  {
    id: 'newsboy-cap',
    label: 'Newsboy Cap',
    body: painted('#EF5D78', '<path d="M13 18C16 8 24 4 34 5c10 1 16 6 17 14H13Z"/><path d="M25 6l2 13M36 6l-1 13M45 10l-4 9"/><path d="M33 18h22c-3 5-10 6-22 4v-4Z"/>')
  },
  {
    id: 'cowboy-hat',
    label: 'Cowboy Hat',
    body: painted('#D78B3F', '<path d="M20 5c4 3 20 3 24 0l4 17H16L20 5Z"/><path d="M17 15h30"/><path d="M4 20c8 4 17 3 28 1 11 2 20 3 28-1-3 7-11 9-28 7-17 2-25 0-28-7Z"/>')
  },
  {
    id: 'sailor-cap',
    label: 'Sailor Cap',
    body: painted('#F5F7FB', '<path d="M17 11c7-7 23-7 30 0l4 10H13l4-10Z"/><path fill="#3478F6" d="M13 18h38v7H13z"/><path d="M22 10l4 8M42 10l-4 8"/>')
  },
  {
    id: 'sun-hat',
    label: 'Sun Hat',
    body: painted('#F2C95C', '<path d="M21 7h22l5 15H16l5-15Z"/><path d="M15 16h34"/><path d="M4 22c10 3 19 2 28 0 9 2 18 3 28 0l-4 6H8l-4-6Z"/>')
  },
  {
    id: 'top-hat',
    label: 'Top Hat',
    body: painted('#3C4658', '<path d="M19 2h26l3 21H16L19 2Z"/><path fill="#7B61FF" d="M16 16h32v7H16z"/><path d="M8 22h48v5H8z"/>')
  },
  {
    id: 'crown',
    label: 'Crown',
    body: painted('#F3B83F', '<path d="M12 22 9 6l12 8L32 3l11 11 12-8-3 16H12Z"/><path d="M13 18h38v7H13z"/><circle cx="21" cy="19" r="1.5" fill="#EF5D78"/><circle cx="32" cy="19" r="1.5" fill="#3478F6"/><circle cx="43" cy="19" r="1.5" fill="#20B8A6"/>')
  },
  {
    id: 'party-hat',
    label: 'Party Hat',
    body: painted('#EF5D78', '<path d="M32 3 48 25H16L32 3Z"/><path d="m25 12 15 5M21 19l16 5"/><circle cx="32" cy="3" r="3" fill="#F3B83F"/>')
  },
  {
    id: 'wizard-hat',
    label: 'Wizard Hat',
    body: painted('#6C55D9', '<path d="M21 22C26 13 29 7 31 1c8 7 13 13 14 21H21Z"/><path d="M31 1c7 1 11 0 14-3-1 6-4 9-9 11"/><path fill="#F3B83F" d="M16 18h32v6H16z"/><path d="M7 23h50l-5 5H12l-5-5Z"/>')
  },
  {
    id: 'flat-cap',
    label: 'Flat Cap',
    body: painted('#42C96B', '<path d="M13 17C17 8 24 5 34 5c10 0 17 5 18 13H13Z"/><path d="M25 6v12M38 6l-2 12"/><path d="M31 17h25c-4 6-12 7-25 5v-5Z"/>')
  }
];

const hair = [
  {
    id: 'side-swept',
    label: 'Side Swept',
    body: painted('#4B354F', '<path d="M12 23C14 10 24 5 39 6c9 1 14 5 16 12-9-3-15-1-21 3 2-5 1-8-1-10-4 8-11 12-21 12Z"/>')
  },
  {
    id: 'quiff',
    label: 'Quiff',
    body: painted('#D89A35', '<path d="M13 23c1-8 6-12 14-13-2-5 0-8 5-9 0 5 2 7 5 8 1-6 5-8 10-7-4 3-5 7-3 11 5 1 8 4 9 10H13Z"/>')
  },
  {
    id: 'mohawk',
    label: 'Mohawk',
    body: painted('#EF5D78', '<path d="M22 22 24 9l5 4 3-12 4 12 6-5 2 14H22Z"/>')
  },
  {
    id: 'twin-tufts',
    label: 'Twin Tufts',
    body: painted('#2B3C66', '<path d="M13 22c2-9 6-14 13-17l2 10 4-13 4 13 5-10c7 4 10 9 11 17H13Z"/>')
  },
  {
    id: 'single-curl',
    label: 'Single Curl',
    body: painted('#8A5B3D', '<path d="M13 23c1-9 7-14 17-15 5-6 14-6 18-1 4 5 0 11-5 11-4 0-6-4-4-7-7 1-8 7-7 12H13Z"/><path d="M32 9c-3 4-3 8 0 12"/>')
  },
  {
    id: 'slick-back',
    label: 'Slick Back',
    body: painted('#293548', '<path d="M12 23c3-12 13-18 30-18 5 0 9 1 12 3-9 1-16 5-21 13-5-5-12-5-21 2Z"/><path d="M23 9c2 4 2 8 0 12M32 7c2 4 2 8 0 12"/>')
  },
  {
    id: 'messy-spikes',
    label: 'Messy Spikes',
    body: painted('#6C55D9', '<path d="m11 23 5-14 6 6 4-13 6 10 7-11 1 13 10-7-3 16H11Z"/>')
  },
  {
    id: 'bowl-cut',
    label: 'Bowl Cut',
    body: painted('#3F2D28', '<path d="M11 19C14 8 22 4 32 4s18 4 21 15c-6-3-12-4-21-4s-15 1-21 4Z"/><path d="M13 18v6M51 18v6"/>')
  },
  {
    id: 'center-part',
    label: 'Center Part',
    body: painted('#C8D2DB', '<path d="M12 23C13 11 21 5 31 5v18c-4-4-10-5-19 0ZM52 23C51 11 43 5 33 5v18c4-4 10-5 19 0Z"/>')
  },
  {
    id: 'wave',
    label: 'Wave',
    body: painted('#3478F6', '<path d="M12 23C14 11 21 5 31 6c7 0 11-4 11-8 7 5 7 12 2 16 5 0 8 3 9 9-8-4-14-4-20 0-5-6-12-6-21 0Z"/>')
  },
  {
    id: 'top-knot',
    label: 'Top Knot',
    body: painted('#4B354F', '<circle cx="32" cy="5" r="5"/><path d="M13 23c1-10 8-15 19-15s18 5 19 15H13Z"/><path d="M27 8c1 5 1 10-1 15M37 8c-1 5-1 10 1 15"/>')
  },
  {
    id: 'pompadour',
    label: 'Pompadour',
    body: painted('#D89A35', '<path d="M11 22c2-7 7-11 14-12 1-7 6-10 12-9 8 1 11 8 8 14 5 0 8 3 9 7H11Z"/><path d="M25 10c6 0 11 2 15 6"/>')
  },
  {
    id: 'afro-puff',
    label: 'Afro Puff',
    body: painted('#50362C', '<circle cx="18" cy="15" r="8"/><circle cx="28" cy="9" r="9"/><circle cx="39" cy="9" r="9"/><circle cx="48" cy="16" r="8"/><path d="M12 23c3-8 10-12 20-12s17 4 20 12H12Z"/>')
  },
  {
    id: 'lightning-fringe',
    label: 'Lightning Fringe',
    body: painted('#F3B83F', '<path d="M11 22C13 10 21 5 33 5h18l-9 7h9l-14 11 2-9-9 7 1-10c-7 1-12 5-20 11Z"/>')
  },
  {
    id: 'short-crop',
    label: 'Short Crop',
    body: painted('#243042', '<path d="M13 22c1-10 8-16 19-16s18 6 19 16l-5-4-5 4-5-4-5 4-5-4-5 4-4-4-4 4Z"/>')
  }
];

const toolDefs = [
  { id: 'code', label: 'Code', accent: '#7B61FF', glyph: '<path d="m49 44-5 5 5 5M55 44l5 5-5 5M54 42l-4 14"/>' },
  { id: 'terminal', label: 'Terminal', accent: '#3478F6', glyph: '<path d="m45 45 5 4-5 4M52 54h7"/>' },
  { id: 'review', label: 'Review', accent: '#22B8F0', glyph: '<circle cx="49" cy="48" r="4"/><path d="m52 51 6 6M54 44l2 2 4-4"/>' },
  { id: 'debug', label: 'Debug', accent: '#42C96B', glyph: '<rect x="47" y="45" width="10" height="11" rx="5"/><path d="M50 45v-3M54 45v-3M44 47h3M44 52h3M57 47h3M57 52h3M49 50h6"/>' },
  { id: 'test', label: 'Test', accent: '#A7D92D', glyph: '<path d="M49 42h6M51 42v5l-6 10h14l-6-10v-5M48 52h8"/>' },
  { id: 'research', label: 'Research', accent: '#22B8F0', glyph: '<circle cx="50" cy="49" r="6"/><path d="m54 54 5 5"/>' },
  { id: 'browser', label: 'Browser', accent: '#3478F6', glyph: '<circle cx="52" cy="50" r="8"/><path d="M44 50h16M52 42c3 3 3 13 0 16M52 42c-3 3-3 13 0 16"/>' },
  { id: 'writing', label: 'Writing', accent: '#F3B83F', glyph: '<path d="m45 56 2-6 9-9 4 4-9 9-6 2ZM54 43l4 4"/>' },
  { id: 'docs', label: 'Documents', accent: '#22B8F0', glyph: '<path d="M46 41h9l5 5v13H46V41Z"/><path d="M55 41v5h5M49 51h8M49 55h6"/>' },
  { id: 'plan', label: 'Planning', accent: '#F3B83F', glyph: '<rect x="50" y="41" width="5" height="5" rx="1"/><rect x="44" y="54" width="5" height="5" rx="1"/><rect x="57" y="54" width="5" height="5" rx="1"/><path d="M52.5 46v4M46.5 50h13M46.5 50v4M59.5 50v4"/>' },
  { id: 'data', label: 'Data', accent: '#20B8A6', glyph: '<path d="M45 57V51h4v6M51 57V46h4v11M57 57V42h4v15"/>' },
  { id: 'database', label: 'Database', accent: '#20B8A6', glyph: '<ellipse cx="52" cy="44" rx="8" ry="3"/><path d="M44 44v12c0 2 4 3 8 3s8-1 8-3V44M44 50c0 2 4 3 8 3s8-1 8-3"/>' },
  { id: 'design', label: 'Design', accent: '#EF5D78', glyph: '<path d="m52 41 7 7-7 11-7-11 7-7ZM52 41v12M48 52h8"/>' },
  { id: 'image', label: 'Image', accent: '#EF5D78', glyph: '<rect x="43" y="42" width="18" height="16" rx="2"/><circle cx="48" cy="47" r="2"/><path d="m45 56 5-5 4 3 3-4 4 6"/>' },
  { id: 'chat', label: 'Communication', accent: '#22B8F0', glyph: '<path d="M44 43h16v12H51l-5 4v-4h-2V43Z"/><circle cx="49" cy="49" r="1" fill="#F5F7FB" stroke="none"/><circle cx="53" cy="49" r="1" fill="#F5F7FB" stroke="none"/><circle cx="57" cy="49" r="1" fill="#F5F7FB" stroke="none"/>' },
  { id: 'deploy', label: 'Deploy', accent: '#F47A32', glyph: '<path d="M54 41c4 3 6 7 5 12l-7 5-7-7 5-7 4-3Z"/><circle cx="54" cy="48" r="2"/><path d="m47 54-3 4 5-2M51 58l-2 3"/>' },
  { id: 'build', label: 'Build', accent: '#F3B83F', glyph: '<path d="m44 46 4-4 5 5-4 4M49 51l8 8M54 44h6v6"/>' },
  { id: 'git', label: 'Git', accent: '#F47A32', glyph: '<circle cx="47" cy="44" r="2"/><circle cx="57" cy="48" r="2"/><circle cx="47" cy="57" r="2"/><path d="M47 46v9M49 48h6M52 48v-4"/>' },
  { id: 'files', label: 'Files', accent: '#3478F6', glyph: '<path d="M43 45h7l2 2h9v11H43V45Z"/><path d="M43 48h18"/>' },
  { id: 'automation', label: 'Automation', accent: '#7B61FF', glyph: '<circle cx="52" cy="50" r="4"/><path d="M52 41v4M52 55v4M43 50h4M57 50h4M46 44l3 3M55 53l3 3M58 44l-3 3M49 53l-3 3"/>' },
  { id: 'security', label: 'Security', accent: '#42C96B', glyph: '<path d="M52 41 60 44v6c0 5-3 8-8 10-5-2-8-5-8-10v-6l8-3Z"/><path d="m48 50 3 3 5-6"/>' },
  { id: 'config', label: 'Configuration', accent: '#8D9AAA', glyph: '<path d="M45 44h15M45 50h15M45 56h15"/><circle cx="50" cy="44" r="2" fill="#F5F7FB"/><circle cx="56" cy="50" r="2" fill="#F5F7FB"/><circle cx="48" cy="56" r="2" fill="#F5F7FB"/>' },
  { id: 'monitor', label: 'Monitor', accent: '#42C96B', glyph: '<path d="M43 52h4l2-6 4 11 3-7h5"/>' },
  { id: 'general', label: 'General', accent: '#8D9AAA', glyph: '<path d="M58 42a6 6 0 0 1-7 8l-7 7 3 3 7-7a6 6 0 0 1 8-7l-4 4-4-4 4-4Z"/>' }
];

const statuses = [
  { id: 'running', label: 'Running', color: '#16C8EA', frames: 6, fps: 6, loop: true, holdLast: false },
  { id: 'problem', label: 'Problem', color: '#FFB020', frames: 6, fps: 3, loop: true, holdLast: false },
  { id: 'success', label: 'Success', color: '#9AD51F', frames: 6, fps: 8, loop: false, holdLast: true },
  { id: 'failure', label: 'Failure', color: '#FF4D4F', frames: 6, fps: 8, loop: false, holdLast: true },
  { id: 'cleanup', label: 'Cleanup', color: '#9098A3', frames: 6, fps: 2, loop: true, holdLast: false }
];

function svg(body, title = '') {
  const titleNode = title ? '<title>' + escapeXml(title) + '</title>' : '';
  return '<svg xmlns="http://www.w3.org/2000/svg" viewBox="' + VIEWBOX + '" width="64" height="64" fill="none">' + titleNode + body + '</svg>';
}

function escapeXml(value) {
  return String(value)
    .replaceAll('&', '&amp;')
    .replaceAll('<', '&lt;')
    .replaceAll('>', '&gt;')
    .replaceAll('"', '&quot;');
}

function mix(hex, target, amount) {
  const source = hex.replace('#', '');
  const dest = target.replace('#', '');
  const values = [0, 2, 4].map(function (offset) {
    const a = parseInt(source.slice(offset, offset + 2), 16);
    const b = parseInt(dest.slice(offset, offset + 2), 16);
    return Math.round(a + (b - a) * amount).toString(16).padStart(2, '0');
  });
  return '#' + values.join('');
}

function frameBody(color) {
  const light = mix(color, '#FFFFFF', 0.18);
  const dark = mix(color, '#000000', 0.2);
  return [
    '<g stroke="' + INK + '" stroke-width="2.4" stroke-linejoin="round">',
    '<rect x="2" y="29" width="7" height="16" rx="3.5" fill="' + dark + '"/>',
    '<rect x="55" y="29" width="7" height="16" rx="3.5" fill="' + dark + '"/>',
    '<rect x="5" y="14" width="54" height="44" rx="13" fill="' + color + '"/>',
    '<path d="M12 20c8-4 32-5 44 1" stroke="' + light + '" stroke-width="3" fill="none"/>',
    '<rect x="10" y="22" width="44" height="29" rx="9" fill="' + FACE + '" stroke="' + FACE_EDGE + '"/>',
    '<path d="M49 58h10V48L49 58Z" fill="' + FACE + '"/>',
    '<path d="M22 14h20" stroke="' + dark + '" stroke-width="2"/>',
    '</g>'
  ].join('');
}

function toolBody(tool) {
  return [
    '<g stroke-linecap="round" stroke-linejoin="round">',
    '<rect x="39.5" y="37.5" width="24" height="24" rx="6" fill="#171C25" stroke="' + INK + '" stroke-width="2.5"/>',
    '<rect x="41" y="39" width="21" height="21" rx="5" fill="none" stroke="' + tool.accent + '" stroke-width="1.5"/>',
    '<g fill="none" stroke="' + WHITE + '" stroke-width="2.6">' + tool.glyph + '</g>',
    '</g>'
  ].join('');
}

function workspaceBadgeBody(color, glyph = 'diamond') {
  const glyphs = {
    diamond: '<path d="m9 45 6 6-6 6-6-6 6-6Z"/>',
    lines: '<path d="M4 47h10M4 51h10M4 55h10"/>',
    cube: '<path d="m9 44 6 4v7l-6 4-6-4v-7l6-4ZM3 48l6 4 6-4M9 52v7"/>',
    ring: '<circle cx="9" cy="51" r="4"/>'
  };
  return '<g stroke="' + INK + '" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><circle cx="9" cy="51" r="9" fill="' + color + '"/><g fill="none" stroke="' + WHITE + '" stroke-width="2">' + glyphs[glyph] + '</g></g>';
}

const leftGridX = [17, 20, 23, 26, 29];
const rightGridX = [35, 38, 41, 44, 47];
const gridY = [28, 31, 34, 37, 40];
const arrow = [[0, 0], [1, 1], [2, 2], [1, 3], [0, 4]];
const exclamation = [[2, 0], [2, 1], [2, 2], [2, 4]];
const diamond = [[2, 0], [1, 1], [3, 1], [0, 2], [4, 2], [1, 3], [3, 3], [2, 4]];
const happy = [[0, 3], [1, 2], [2, 1], [3, 2], [4, 3]];
const cross = [[0, 0], [4, 0], [1, 1], [3, 1], [2, 2], [1, 3], [3, 3], [0, 4], [4, 4]];
const cleanupLine = [[1, 2], [2, 2], [3, 2]];

function dotGrid() {
  const dots = [];
  for (const xs of [leftGridX, rightGridX]) {
    for (let row = 0; row < 5; row += 1) {
      for (let col = 0; col < 5; col += 1) {
        dots.push('<circle cx="' + xs[col] + '" cy="' + gridY[row] + '" r="1" fill="' + MUTED_LED + '" opacity=".62"/>');
      }
    }
  }
  return dots.join('');
}

function patternDots(xs, pattern, color, opacity = 1, shift = 0, limit = pattern.length) {
  return pattern.slice(0, limit).map(function (point) {
    const col = Math.max(0, Math.min(4, point[0] + shift));
    return '<circle cx="' + xs[col] + '" cy="' + gridY[point[1]] + '" r="1.25" fill="' + color + '" opacity="' + opacity + '"/>';
  }).join('');
}

function eyeFrameBody(status, frameIndex) {
  let active = '';
  if (status.id === 'running') {
    const shift = frameIndex % 2;
    const opacity = [0.6, 0.8, 1, 0.8, 0.6, 0.8][frameIndex];
    active += patternDots(leftGridX, arrow, status.color, opacity, shift);
    active += patternDots(rightGridX, arrow, status.color, opacity, shift);
  } else if (status.id === 'problem') {
    const opacity = [0.45, 0.7, 1, 0.7, 0.45, 0.7][frameIndex];
    active += patternDots(leftGridX, exclamation, status.color, opacity);
    active += patternDots(rightGridX, diamond, status.color, opacity);
  } else if (status.id === 'success') {
    const limit = Math.max(1, Math.ceil(happy.length * (frameIndex + 1) / status.frames));
    active += patternDots(leftGridX, happy, status.color, 1, 0, limit);
    active += patternDots(rightGridX, happy, status.color, 1, 0, limit);
  } else if (status.id === 'failure') {
    const limit = Math.max(1, Math.ceil(cross.length * (frameIndex + 1) / status.frames));
    active += patternDots(leftGridX, cross, status.color, 1, 0, limit);
    active += patternDots(rightGridX, cross, status.color, 1, 0, limit);
  } else {
    const yShift = frameIndex === 2 || frameIndex === 3 ? 1 : 0;
    const opacity = [0.4, 0.55, 0.7, 0.7, 0.55, 0.4][frameIndex];
    const shifted = cleanupLine.map(function (point) { return [point[0], Math.min(4, point[1] + yShift)]; });
    active += patternDots(leftGridX, shifted, status.color, opacity);
    active += patternDots(rightGridX, shifted, status.color, opacity);
  }
  return '<g>' + dotGrid() + active + '</g>';
}

async function resetGeneratedDirs() {
  const expected = path.resolve(here, 'assets');
  const actual = path.resolve(outRoot);
  if (actual !== expected || !actual.startsWith(path.resolve(here) + path.sep)) {
    throw new Error('Unsafe asset output path: ' + actual);
  }
  await fs.mkdir(outRoot, { recursive: true });
  for (const generatedDir of [svgRoot, pngRoot, spriteRoot, previewRoot]) {
    const resolvedDir = path.resolve(generatedDir);
    if (!resolvedDir.startsWith(actual + path.sep)) {
      throw new Error('Unsafe generated directory: ' + resolvedDir);
    }
    await fs.rm(resolvedDir, { recursive: true, force: true });
  }
  await fs.rm(path.join(outRoot, 'manifest.json'), { force: true });
  await Promise.all([svgRoot, pngRoot, spriteRoot, previewRoot].map(function (dir) {
    return fs.mkdir(dir, { recursive: true });
  }));
}

async function writeSvgAndPng(relativePath, svgText) {
  if (/linearGradient|radialGradient|filter=|drop-shadow/i.test(svgText)) {
    throw new Error('Disallowed non-flat SVG effect in ' + relativePath);
  }
  const svgPath = path.join(svgRoot, relativePath + '.svg');
  const pngPath = path.join(pngRoot, relativePath + '.png');
  await fs.mkdir(path.dirname(svgPath), { recursive: true });
  await fs.mkdir(path.dirname(pngPath), { recursive: true });
  await fs.writeFile(svgPath, svgText, 'utf8');
  await sharp(Buffer.from(svgText)).resize(64, 64).png().toFile(pngPath);
  return {
    svg: path.relative(outRoot, svgPath).replaceAll('\\', '/'),
    png: path.relative(outRoot, pngPath).replaceAll('\\', '/')
  };
}

async function writeEyeStatus(status) {
  const frames = [];
  const pngBuffers = [];
  for (let index = 0; index < status.frames; index += 1) {
    const body = eyeFrameBody(status, index);
    const svgText = svg(body, status.label + ' frame ' + index);
    const relative = path.join('eyes', status.id, 'frame-' + String(index).padStart(2, '0'));
    const files = await writeSvgAndPng(relative, svgText);
    frames.push(files);
    pngBuffers.push(await sharp(Buffer.from(svgText)).resize(64, 64).png().toBuffer());
  }
  const spritePath = path.join(spriteRoot, 'eyes', status.id + '.png');
  await fs.mkdir(path.dirname(spritePath), { recursive: true });
  await sharp({
    create: {
      width: 64 * status.frames,
      height: 64,
      channels: 4,
      background: { r: 0, g: 0, b: 0, alpha: 0 }
    }
  }).composite(pngBuffers.map(function (input, index) {
    return { input: input, left: index * 64, top: 0 };
  })).png().toFile(spritePath);
  return {
    id: status.id,
    label: status.label,
    color: status.color,
    fps: status.fps,
    loop: status.loop,
    holdLast: status.holdLast,
    frameWidth: 64,
    frameHeight: 64,
    frames: frames,
    sprite: path.relative(outRoot, spritePath).replaceAll('\\', '/')
  };
}

function previewShell(title, width, height, content) {
  return [
    '<svg xmlns="http://www.w3.org/2000/svg" width="' + width + '" height="' + height + '" viewBox="0 0 ' + width + ' ' + height + '">',
    '<rect width="100%" height="100%" fill="#10141C"/>',
    '<style>text{font-family:Segoe UI,Arial,sans-serif;fill:#E9EDF4}.h{font-size:28px;font-weight:700}.s{font-size:19px;font-weight:700}.l{font-size:13px;fill:#AEB7C4}</style>',
    '<text x="32" y="42" class="h">' + escapeXml(title) + '</text>',
    content,
    '</svg>'
  ].join('');
}

function previewCell(body, label, x, y, scale = 1) {
  return '<g transform="translate(' + x + ' ' + y + ')"><g transform="scale(' + scale + ')">' + body + '</g><text x="' + (70 * scale) + '" y="' + (34 * scale) + '" class="l">' + escapeXml(label) + '</text></g>';
}

function sectionTitle(label, y) {
  return '<text x="32" y="' + y + '" class="s">' + escapeXml(label) + '</text>';
}

async function renderPreview(name, svgText, width) {
  const svgPath = path.join(previewRoot, name + '.svg');
  const pngPath = path.join(previewRoot, name + '.png');
  await fs.writeFile(svgPath, svgText, 'utf8');
  await sharp(Buffer.from(svgText)).resize({ width: width }).png().toFile(pngPath);
}

function staticEye(statusId) {
  const status = statuses.find(function (item) { return item.id === statusId; });
  const index = status.id === 'success' || status.id === 'failure' ? status.frames - 1 : 2;
  return eyeFrameBody(status, index);
}

async function makeCatalogPreview() {
  const cells = [];
  let y = 78;
  const columns = 5;
  const cellW = 224;
  const cellH = 88;
  const neutralFrame = frameBody(frameColors[0].color);

  cells.push(sectionTitle('15 Hats', y));
  y += 24;
  hats.forEach(function (item, index) {
    const col = index % columns;
    const row = Math.floor(index / columns);
    cells.push(previewCell(neutralFrame + staticEye('running') + item.body, item.id, 28 + col * cellW, y + row * cellH));
  });
  y += Math.ceil(hats.length / columns) * cellH + 28;

  cells.push(sectionTitle('15 Hair Styles', y));
  y += 24;
  hair.forEach(function (item, index) {
    const col = index % columns;
    const row = Math.floor(index / columns);
    cells.push(previewCell(neutralFrame + staticEye('success') + item.body, item.id, 28 + col * cellW, y + row * cellH));
  });
  y += Math.ceil(hair.length / columns) * cellH + 28;

  cells.push(sectionTitle('9 Frame Colors', y));
  y += 24;
  frameColors.forEach(function (item, index) {
    const col = index % columns;
    const row = Math.floor(index / columns);
    cells.push(previewCell(frameBody(item.color) + staticEye('running'), item.id, 28 + col * cellW, y + row * cellH));
  });
  y += Math.ceil(frameColors.length / columns) * cellH + 28;

  cells.push(sectionTitle('24 Task Tools', y));
  y += 24;
  toolDefs.forEach(function (item, index) {
    const col = index % columns;
    const row = Math.floor(index / columns);
    cells.push(previewCell(neutralFrame + staticEye('running') + toolBody(item), item.id, 28 + col * cellW, y + row * cellH));
  });
  y += Math.ceil(toolDefs.length / columns) * cellH + 30;

  const catalog = previewShell('Agent Icon Asset Catalog', 1160, y, cells.join(''));
  await renderPreview('asset-catalog', catalog, 1160);
}

async function makeEyePreview() {
  const width = 1050;
  const content = [];
  let y = 80;
  statuses.forEach(function (status) {
    content.push('<text x="30" y="' + (y + 38) + '" class="s">' + escapeXml(status.label) + '</text>');
    for (let frame = 0; frame < status.frames; frame += 1) {
      const x = 185 + frame * 135;
      const body = frameBody('#E7EBF0') + eyeFrameBody(status, frame);
      content.push('<g transform="translate(' + x + ' ' + y + ')">' + body + '<text x="23" y="78" class="l">F' + frame + '</text></g>');
    }
    y += 116;
  });
  const preview = previewShell('LED Eye Animation Frames', width, y + 10, content.join(''));
  await renderPreview('eye-animation-frames', preview, width);
}

async function makeCombinationPreview() {
  const width = 1100;
  const content = [];
  const workspace = [
    ['#7B61FF', 'diamond'],
    ['#3478F6', 'lines'],
    ['#42C96B', 'cube'],
    ['#F47A32', 'ring']
  ];
  for (let index = 0; index < 15; index += 1) {
    const x = 55 + (index % 5) * 210;
    const y = 80 + Math.floor(index / 5) * 150;
    const frame = frameColors[index % frameColors.length];
    const head = index % 2 === 0 ? hats[index % hats.length] : hair[index % hair.length];
    const tool = toolDefs[(index * 3) % toolDefs.length];
    const status = statuses[index % statuses.length];
    const frameIndex = status.id === 'success' || status.id === 'failure' ? status.frames - 1 : 2;
    const workspaceItem = workspace[index % workspace.length];
    const body = frameBody(frame.color)
      + head.body
      + eyeFrameBody(status, frameIndex)
      + workspaceBadgeBody(workspaceItem[0], workspaceItem[1])
      + toolBody(tool);
    content.push('<g transform="translate(' + x + ' ' + y + ') scale(1.25)">' + body + '</g>');
    content.push('<text x="' + (x + 3) + '" y="' + (y + 94) + '" class="l">' + escapeXml(head.id + ' / ' + tool.id) + '</text>');
  }
  const preview = previewShell('Composed Agent Icon Examples', width, 540, content.join(''));
  await renderPreview('combinations', preview, width);
}

async function makeSmallSizePreview() {
  const width = 400;
  const height = 220;
  const content = [];
  const workspace = [
    ['#7B61FF', 'diamond'],
    ['#3478F6', 'lines'],
    ['#42C96B', 'cube'],
    ['#F47A32', 'ring']
  ];
  for (let index = 0; index < 15; index += 1) {
    const x = 24 + (index % 5) * 76;
    const y = 52 + Math.floor(index / 5) * 52;
    const frame = frameColors[index % frameColors.length];
    const head = index % 2 === 0 ? hats[index % hats.length] : hair[index % hair.length];
    const tool = toolDefs[(index * 5) % toolDefs.length];
    const status = statuses[index % statuses.length];
    const frameIndex = status.id === 'success' || status.id === 'failure' ? status.frames - 1 : 2;
    const workspaceItem = workspace[index % workspace.length];
    const body = frameBody(frame.color)
      + head.body
      + eyeFrameBody(status, frameIndex)
      + workspaceBadgeBody(workspaceItem[0], workspaceItem[1])
      + toolBody(tool);
    content.push('<g transform="translate(' + x + ' ' + y + ') scale(.5)">' + body + '</g>');
  }
  const preview = previewShell('32 px Legibility Check', width, height, content.join(''));
  const svgPath = path.join(previewRoot, 'small-size-check.svg');
  const pngPath = path.join(previewRoot, 'small-size-check.png');
  const zoomPath = path.join(previewRoot, 'small-size-check-2x.png');
  await fs.writeFile(svgPath, preview, 'utf8');
  await sharp(Buffer.from(preview)).png().toFile(pngPath);
  await sharp(pngPath).resize(width * 2, height * 2, { kernel: 'nearest' }).png().toFile(zoomPath);
}

async function main() {
  if (hats.length !== 15 || hair.length !== 15 || frameColors.length !== 9 || toolDefs.length !== 24) {
    throw new Error('Asset count contract failed');
  }
  await resetGeneratedDirs();

  const manifest = {
    version: 1,
    canvas: { width: 64, height: 64, viewBox: VIEWBOX },
    format: {
      source: 'SVG',
      raster: 'PNG 64x64',
      transparent: true,
      flat: true,
      gradients: false,
      shadows: false
    },
    layerOrder: ['frame', 'headwear', 'eyes', 'workspaceBadge', 'taskTool'],
    identityRule: {
      headwear: 'Choose exactly one hat or one hair style.',
      stableSeed: 'sessionId',
      semantic: false
    },
    hats: [],
    hair: [],
    frames: [],
    tools: [],
    eyes: [],
    templates: {}
  };

  for (const item of hats) {
    const files = await writeSvgAndPng(path.join('hats', item.id), svg(item.body, item.label));
    manifest.hats.push({ id: item.id, label: item.label, ...files });
  }
  for (const item of hair) {
    const files = await writeSvgAndPng(path.join('hair', item.id), svg(item.body, item.label));
    manifest.hair.push({ id: item.id, label: item.label, ...files });
  }
  for (const item of frameColors) {
    const files = await writeSvgAndPng(path.join('frames', item.id), svg(frameBody(item.color), item.label + ' frame'));
    manifest.frames.push({ id: item.id, label: item.label, color: item.color, ...files });
  }
  for (const item of toolDefs) {
    const files = await writeSvgAndPng(path.join('tools', item.id), svg(toolBody(item), item.label + ' task tool'));
    manifest.tools.push({ id: item.id, label: item.label, accent: item.accent, ...files });
  }
  for (const status of statuses) {
    manifest.eyes.push(await writeEyeStatus(status));
  }

  const workspaceTemplate = await writeSvgAndPng(path.join('templates', 'workspace-badge'), svg(workspaceBadgeBody('#667085', 'diamond'), 'Workspace badge template'));
  const neutralEyes = await writeSvgAndPng(path.join('templates', 'neutral-led-grid'), svg(dotGrid(), 'Neutral LED grid'));
  manifest.templates.workspaceBadge = workspaceTemplate;
  manifest.templates.neutralLedGrid = neutralEyes;

  await makeCatalogPreview();
  await makeEyePreview();
  await makeCombinationPreview();
  await makeSmallSizePreview();

  await fs.writeFile(path.join(outRoot, 'manifest.json'), JSON.stringify(manifest, null, 2) + '\n', 'utf8');

  const summary = {
    hats: manifest.hats.length,
    hair: manifest.hair.length,
    frames: manifest.frames.length,
    tools: manifest.tools.length,
    eyeStatuses: manifest.eyes.length,
    eyeFrames: manifest.eyes.reduce(function (sum, item) { return sum + item.frames.length; }, 0)
  };
  process.stdout.write(JSON.stringify(summary) + '\n');
}

main().catch(function (error) {
  console.error(error);
  process.exitCode = 1;
});
