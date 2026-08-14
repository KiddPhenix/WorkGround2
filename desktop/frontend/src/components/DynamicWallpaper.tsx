import { useEffect, useRef } from "react";

// ── scene definitions ──────────────────────────────────────────────
// Each scene supplies only its fragment shader; the shared engine
// owns the vertex shader, RAF loop, resize, context-loss, theme, and
// reduced-motion lifecycle.

export const DYNAMIC_WALLPAPER_SCENES = [
  "waves",
  "aurora",
  "nebula",
  "starfield",
  "blackhole",
  "moonclouds",
  "biolume",
  "dunes",
] as const;

export type SceneName = (typeof DYNAMIC_WALLPAPER_SCENES)[number];

export function isSceneName(value: string): value is SceneName {
  return (DYNAMIC_WALLPAPER_SCENES as readonly string[]).includes(value);
}

interface SceneDef {
  fragmentShader: string;
}

const VERT = `#version 100
attribute vec2 a_pos;
varying vec2 v_uv;
void main() {
  v_uv = a_pos * 0.5 + 0.5;
  gl_Position = vec4(a_pos, 0.0, 1.0);
}`;

// ── shared GLSL utilities (prepended to every fragment shader) ─────

const GLSL_COMMON = `
float hash(vec2 p) {
  return fract(sin(dot(p, vec2(127.1, 311.7))) * 43758.5453);
}

float noise(vec2 p) {
  vec2 i = floor(p);
  vec2 f = fract(p);
  f = f * f * (3.0 - 2.0 * f);
  return mix(
    mix(hash(i), hash(i + vec2(1.0, 0.0)), f.x),
    mix(hash(i + vec2(0.0, 1.0)), hash(i + vec2(1.0, 1.0)), f.x),
    f.y
  );
}

float fbm(vec2 p) {
  float v = 0.0;
  float a = 0.5;
  mat2 m = mat2(1.6, -1.2, 1.2, 1.6);
  for (int k = 0; k < 4; k++) {
    v += a * noise(p);
    p = m * p * 2.0;
    a *= 0.5;
  }
  return v;
}

float hash11(float n) {
  return fract(sin(n) * 43758.5453123);
}

vec2 hash22(vec2 p) {
  return fract(sin(vec2(
    dot(p, vec2(127.1, 311.7)),
    dot(p, vec2(269.5, 183.3))
  )) * 43758.5453);
}

mat2 rotate2d(float angle) {
  float s = sin(angle);
  float c = cos(angle);
  return mat2(c, -s, s, c);
}

float softDisc(vec2 p, float radius, float feather) {
  return 1.0 - smoothstep(radius - feather, radius + feather, length(p));
}
`;

function frag(src: string) {
  return src.replace("precision highp float;", `precision highp float;${GLSL_COMMON}`);
}

// ── waves ──────────────────────────────────────────────────────────

const FRAG_WAVES = frag(`#version 100
precision highp float;
varying vec2 v_uv;
uniform vec2 u_res;
uniform float u_time;
uniform float u_light;

void main() {
  vec2 uv = vec2(v_uv.x, 1.0 - v_uv.y);
  float aspect = u_res.x / u_res.y;
  float t = u_time * 0.12;
  float depth = smoothstep(0.0, 1.0, uv.y);
  float perspective = mix(0.48, 1.35, depth);
  vec2 p = vec2(uv.x * aspect, uv.y * 1.9);

  float swell = sin(p.x * 2.0 + t * 0.7 + sin(p.y * 1.25 - t * 0.25) * 1.3);
  swell += sin(p.x * 3.1 - t * 0.48 + p.y * 0.75) * 0.52;
  float body = fbm(p * vec2(2.5, 1.7) + vec2(t * 0.52, t * 0.31));
  float cross = sin(p.x * 12.0 + p.y * 4.2 + t * 1.0)
    + sin(p.x * 8.5 - p.y * 5.4 - t * 0.72);
  float height = (swell * 0.30 + (body - 0.5) * 1.08 + cross * 0.09) * perspective;

  float crestSoft = smoothstep(0.055, 0.0, abs(height - 0.14 * perspective));
  float crestHard = smoothstep(0.014, 0.0, abs(height - 0.22 * perspective));
  float trough = smoothstep(-0.46, -0.08, height);

  // Dark: midnight Atlantic. Light: airy turquoise shallows with slate depth.
  vec3 top = mix(vec3(0.055, 0.20, 0.34), vec3(0.72, 0.90, 0.91), u_light);
  vec3 mid = mix(vec3(0.025, 0.105, 0.245), vec3(0.32, 0.68, 0.75), u_light);
  vec3 bottom = mix(vec3(0.008, 0.035, 0.115), vec3(0.12, 0.39, 0.56), u_light);
  vec3 water = mix(top, mid, smoothstep(0.06, 0.58, depth));
  water = mix(water, bottom, smoothstep(0.46, 1.0, depth));
  water *= mix(0.78, 1.08, trough);

  vec3 foam = mix(vec3(0.36, 0.63, 0.80), vec3(0.95, 0.985, 0.96), u_light);
  water = mix(water, foam, crestSoft * mix(0.13, 0.22, u_light));
  water += foam * crestHard * mix(0.10, 0.17, u_light);

  float glintPath = exp(-abs(uv.x - 0.64) * mix(7.5, 4.8, depth));
  float glint = glintPath * crestSoft * (0.28 + 0.72 * body);
  water += glint * mix(vec3(0.18, 0.22, 0.25), vec3(0.22, 0.16, 0.07), u_light) * 0.30;

  float micro = pow(max(0.0, sin(p.x * 20.0 - p.y * 7.0 + t * 1.3) * 0.5 + 0.5), 12.0);
  water += micro * foam * (0.018 + depth * 0.025);

  float vignette = 1.0 - smoothstep(0.48, 1.16, length((uv - 0.5) * vec2(0.78, 1.0)));
  water *= mix(0.82, 1.0, vignette);
  gl_FragColor = vec4(water, 1.0);
}`);

// ── aurora ──────────────────────────────────────────────────────────

const FRAG_AURORA = frag(`#version 100
precision highp float;
varying vec2 v_uv;
uniform vec2 u_res;
uniform float u_time;
uniform float u_light;

void main() {
  vec2 uv = vec2(v_uv.x, 1.0 - v_uv.y);
  float aspect = u_res.x / u_res.y;
  float t = u_time * 0.08;

  vec3 skyTop    = mix(vec3(0.01, 0.02, 0.08), vec3(0.70, 0.82, 0.90), u_light);
  vec3 skyHor    = mix(vec3(0.02, 0.04, 0.12), vec3(0.88, 0.92, 0.91), u_light);
  vec3 sky       = mix(skyTop, skyHor, uv.y);

  float starField = 0.0;
  for (int i = 0; i < 3; i++) {
    float scale = 42.0 + float(i) * 18.0;
    vec2 starUV = vec2(uv.x * aspect, uv.y) * scale + vec2(float(i) * 13.7, float(i) * 7.1);
    vec2 grid = floor(starUV);
    vec2 local = fract(starUV) - 0.5;
    float r = hash(grid + float(i) * 19.3);
    float twinkle = 0.6 + 0.4 * sin(t * 2.0 + r * 50.0);
    starField += smoothstep(0.09, 0.0, length(local)) * step(0.955, r) * twinkle;
  }
  vec3 starColor = mix(vec3(0.6, 0.7, 0.9), vec3(0.22, 0.34, 0.52), u_light);
  sky += starField * starColor * mix(0.6, 0.18, u_light);

  vec3 aurora = vec3(0.0);
  for (int band = 0; band < 4; band++) {
    float bandBase = 0.15 + float(band) * 0.13;
    float yOff = uv.y - bandBase;
    float vertExtent = 0.06 + float(band) * 0.008;

    vec2 np = vec2(uv.x * aspect * 3.5 + t * (0.3 + float(band) * 0.12),
                   uv.y * 4.0 + t * 0.15);
    float displacement = fbm(np) * 0.04;
    float curtain = exp(-abs(yOff + displacement) / vertExtent);

    float foldX = uv.x * aspect * 8.0 + float(band) * 2.7 + t * 0.22;
    float fold = sin(foldX + sin(uv.y * 6.0 + t * 0.4) * 1.3) * 0.5 + 0.5;
    curtain *= 0.6 + 0.4 * fold;

    curtain *= 1.0 - smoothstep(0.0, 0.55, uv.y);

    vec3 bandColor;
    if (band == 0) bandColor = mix(vec3(0.15, 0.72, 0.45), vec3(0.05, 0.50, 0.31), u_light);
    else if (band == 1) bandColor = mix(vec3(0.10, 0.60, 0.75), vec3(0.08, 0.46, 0.62), u_light);
    else if (band == 2) bandColor = mix(vec3(0.35, 0.30, 0.75), vec3(0.38, 0.27, 0.64), u_light);
    else bandColor = mix(vec3(0.12, 0.68, 0.50), vec3(0.13, 0.56, 0.40), u_light);
    aurora += curtain * bandColor * mix(0.55, 0.32, u_light);
  }

  float horizonGlow = exp(-abs(uv.y - 0.02) * 6.0);
  vec3 glowColor = mix(vec3(0.08, 0.25, 0.35), vec3(0.20, 0.52, 0.54), u_light);
  sky += horizonGlow * glowColor * 0.25;

  vec3 color = sky + aurora;

  float vignette = 1.0 - length((uv - 0.5) * vec2(1.0, 0.7)) * 0.3;
  color *= vignette;

  gl_FragColor = vec4(color, 1.0);
}`);

// ── nebula ──────────────────────────────────────────────────────────

const FRAG_NEBULA = frag(`#version 100
precision highp float;
varying vec2 v_uv;
uniform vec2 u_res;
uniform float u_time;
uniform float u_light;

void main() {
  vec2 uv = vec2(v_uv.x, 1.0 - v_uv.y);
  float aspect = u_res.x / u_res.y;
  float t = u_time * 0.04;

  vec3 bg = mix(vec3(0.008, 0.006, 0.025), vec3(0.72, 0.70, 0.84), u_light);

  float stars = 0.0;
  for (int layer = 0; layer < 3; layer++) {
    float depth = float(layer) + 1.0;
    float parallax = 1.0 / depth;
    float scale = 32.0 + float(layer) * 18.0;
    vec2 starUV = vec2(uv.x * aspect, uv.y) * scale;
    starUV += vec2(t * parallax * 0.3, t * parallax * 0.15) + vec2(float(layer) * 11.7, float(layer) * 5.3);
    vec2 grid = floor(starUV);
    vec2 local = fract(starUV) - 0.5;
    float r = hash(grid + float(layer) * 23.1);
    float twinkle = 0.65 + 0.35 * sin(t * 3.0 + r * 60.0 + float(layer) * 11.0);
    float brightness = smoothstep(0.08, 0.0, length(local)) * step(0.95, r) * twinkle;
    stars += brightness * (1.0 - float(layer) * 0.2);
  }
  vec3 starColor = mix(vec3(0.7, 0.75, 0.9), vec3(0.24, 0.25, 0.48), u_light);
  vec3 color = bg + stars * starColor * mix(0.5, 0.16, u_light);

  vec2 nc1 = uv * vec2(aspect * 1.8, 1.8) + vec2(t * 0.08, t * 0.04);
  float cloud1 = fbm(nc1);
  cloud1 = smoothstep(0.35, 0.7, cloud1);

  vec2 nc2 = uv * vec2(aspect * 3.0, 3.0) + vec2(t * 0.12, -t * 0.06) + 5.0;
  float cloud2 = fbm(nc2);
  cloud2 = smoothstep(0.4, 0.65, cloud2);

  vec2 nc3 = uv * vec2(aspect * 5.5, 5.5) + vec2(-t * 0.15, t * 0.1) + 12.0;
  float cloud3 = fbm(nc3);
  cloud3 *= 0.5;

  vec3 nebulaColor1 = mix(vec3(0.25, 0.08, 0.35), vec3(0.34, 0.10, 0.30), u_light);
  vec3 nebulaColor2 = mix(vec3(0.05, 0.18, 0.45), vec3(0.08, 0.24, 0.47), u_light);
  vec3 nebulaColor3 = mix(vec3(0.08, 0.30, 0.35), vec3(0.05, 0.34, 0.31), u_light);

  color += cloud1 * nebulaColor1 * 0.35;
  color += cloud2 * nebulaColor2 * 0.30;
  color += cloud3 * nebulaColor3 * 0.20;

  float vignette = 1.0 - length((uv - 0.5) * vec2(1.0, 0.7)) * 0.35;
  color *= vignette;

  gl_FragColor = vec4(color, 1.0);
}`);


// ── starfield ───────────────────────────────────────────────────────
// Deep parallax star field, Milky Way haze, sparse restrained meteors.

const FRAG_STARFIELD = frag(`#version 100
precision highp float;
varying vec2 v_uv;
uniform vec2 u_res;
uniform float u_time;
uniform float u_light;

vec3 starLayer(vec2 p, float scale, float seed, float drift) {
  vec2 gridUV = p * scale + vec2(drift, -drift * 0.37);
  vec2 cell = floor(gridUV);
  vec2 local = fract(gridUV) - 0.5;
  vec2 rnd = hash22(cell + seed);
  vec2 pos = local - (rnd - 0.5) * 0.72;
  float chance = step(0.79, rnd.x);
  float radius = mix(0.018, 0.055, rnd.y * rnd.y);
  float core = 1.0 - smoothstep(radius * 0.28, radius, length(pos));
  float flare = exp(-abs(pos.x) * 120.0) * exp(-abs(pos.y) * 14.0)
    + exp(-abs(pos.y) * 120.0) * exp(-abs(pos.x) * 14.0);
  flare *= step(0.94, rnd.y) * 0.32;
  float temperature = hash(cell + seed * 3.7);
  vec3 warm = mix(vec3(1.0, 0.72, 0.48), vec3(0.48, 0.22, 0.08), u_light);
  vec3 cool = mix(vec3(0.55, 0.72, 1.0), vec3(0.12, 0.30, 0.62), u_light);
  vec3 tint = mix(warm, cool, smoothstep(0.18, 0.82, temperature));
  float twinkle = 0.72 + 0.28 * sin(u_time * (0.35 + rnd.y) + rnd.x * 42.0);
  return tint * chance * (core * 1.45 + flare) * twinkle;
}

void main() {
  vec2 uv = vec2(v_uv.x, 1.0 - v_uv.y);
  float aspect = u_res.x / u_res.y;
  float t = u_time * 0.018;
  vec2 p = (uv - 0.5) * vec2(aspect, 1.0);

  vec3 top = mix(vec3(0.004, 0.006, 0.025), vec3(0.58, 0.70, 0.86), u_light);
  vec3 bottom = mix(vec3(0.012, 0.008, 0.042), vec3(0.78, 0.70, 0.86), u_light);
  vec3 col = mix(top, bottom, uv.y);

  // A broad diagonal Milky Way with a dark dust lane and clumped star clouds.
  vec2 mw = rotate2d(-0.43) * (p + vec2(0.05, -0.02));
  float band = exp(-abs(mw.y) * 2.7);
  float cloud = fbm(mw * vec2(1.15, 3.8) + vec2(t, -t * 0.35));
  float fine = fbm(mw * vec2(3.4, 8.5) - vec2(t * 0.7, t * 0.2));
  float dust = exp(-abs(mw.y + (fine - 0.5) * 0.11) * 12.0);
  float luminous = band * smoothstep(0.28, 0.78, cloud * 0.78 + fine * 0.42);
  vec3 violet = mix(vec3(0.19, 0.10, 0.34), vec3(0.24, 0.12, 0.40), u_light);
  vec3 cyan = mix(vec3(0.08, 0.22, 0.34), vec3(0.08, 0.30, 0.43), u_light);
  col += luminous * mix(violet, cyan, smoothstep(-0.4, 0.45, mw.x)) * 1.35;
  col *= 1.0 - dust * band * 0.38;
  col += band * vec3(0.055, 0.045, 0.10) * (0.45 + cloud * 0.75);

  // Three genuinely different depth planes create slow camera travel.
  col += starLayer(p, 19.0, 3.0, t * 0.20) * 0.72;
  col += starLayer(p, 37.0, 19.0, t * 0.34) * 0.52;
  col += starLayer(p, 71.0, 47.0, t * 0.55) * 0.34;

  // Rare restrained shooting star crossing the empty upper-right field.
  float cycle = fract(u_time * 0.012);
  float gate = smoothstep(0.04, 0.10, cycle) * (1.0 - smoothstep(0.23, 0.31, cycle));
  vec2 origin = vec2(0.78, 0.16) + vec2(-cycle * 0.45, cycle * 0.24);
  vec2 delta = uv - origin;
  vec2 dir = normalize(vec2(-1.0, 0.52));
  float along = dot(delta, dir);
  float across = abs(dot(delta, vec2(-dir.y, dir.x)));
  float meteor = exp(-across * 180.0) * exp(-max(0.0, along) * 24.0)
    * step(-0.22, along) * step(along, 0.015) * gate;
  col += meteor * mix(vec3(0.65, 0.78, 1.0), vec3(0.18, 0.30, 0.62), u_light) * mix(1.6, 0.7, u_light);

  float vignette = smoothstep(1.08, 0.25, length(p * vec2(0.72, 0.92)));
  col *= 0.68 + 0.32 * vignette;
  gl_FragColor = vec4(col, 1.0);
}`);

// ── blackhole ────────────────────────────────────────────────────────
// Gravitationally-lensed star field, dark event horizon, rotating disk.

const FRAG_BLACKHOLE = frag(`#version 100
precision highp float;
varying vec2 v_uv;
uniform vec2 u_res;
uniform float u_time;
uniform float u_light;

vec3 blackholeStars(vec2 p, float scale, float seed) {
  vec2 g = p * scale;
  vec2 cell = floor(g);
  vec2 local = fract(g) - 0.5;
  vec2 rnd = hash22(cell + seed);
  vec2 pos = local - (rnd - 0.5) * 0.72;
  float star = (1.0 - smoothstep(0.018, 0.060, length(pos))) * step(0.90, rnd.x);
  vec3 cool = mix(vec3(0.48, 0.62, 1.0), vec3(0.16, 0.30, 0.58), u_light);
  vec3 warm = mix(vec3(1.0, 0.74, 0.48), vec3(0.54, 0.25, 0.08), u_light);
  return mix(cool, warm, rnd.y) * star;
}

void main() {
  vec2 uv = vec2(v_uv.x, 1.0 - v_uv.y);
  float aspect = u_res.x / u_res.y;
  float t = u_time * 0.035;
  vec2 center = vec2(0.64, 0.47);
  vec2 p = (uv - center) * vec2(aspect, 1.0);
  float r = length(p);
  float a = atan(p.y, p.x);

  vec3 col = mix(vec3(0.002, 0.003, 0.012), vec3(0.72, 0.77, 0.84), u_light);
  col += fbm(uv * vec2(aspect * 1.6, 1.6) + vec2(t, 0.0))
    * mix(vec3(0.012, 0.014, 0.035), vec3(0.055, 0.045, 0.085), u_light);

  // Pull the background angularly around the mass instead of shrinking it into a dot.
  float bend = 0.075 / max(r, 0.055);
  vec2 warped = center + vec2(cos(a + bend * 0.12), sin(a + bend * 0.12)) * (r + bend * 0.030) / vec2(aspect, 1.0);
  col += blackholeStars(warped, 27.0, 6.0) * 0.65;
  col += blackholeStars(warped + vec2(t * 0.05, 0.0), 49.0, 27.0) * 0.42;

  // The accretion disk has turbulent bands and pronounced Doppler asymmetry.
  vec2 diskP = vec2(p.x, p.y * 5.3);
  float diskR = length(diskP);
  float diskA = atan(diskP.y, diskP.x);
  float grain = fbm(vec2(diskA * 2.6 - t * 5.0, diskR * 17.0));
  float bands = 0.55 + 0.45 * sin(diskR * 115.0 - diskA * 5.0 + grain * 4.0);
  float diskMask = smoothstep(0.12, 0.17, diskR) * (1.0 - smoothstep(0.46, 0.58, diskR));
  diskMask *= smoothstep(0.22, 0.68, grain * 0.72 + bands * 0.45);
  float hot = 1.0 - smoothstep(0.16, 0.49, diskR);
  float doppler = 0.34 + 1.10 * smoothstep(-0.95, 0.85, cos(diskA));
  vec3 outer = mix(vec3(0.72, 0.14, 0.018), vec3(0.68, 0.18, 0.025), u_light);
  vec3 inner = mix(vec3(0.88, 0.55, 0.22), vec3(0.98, 0.68, 0.25), u_light);
  vec3 diskColor = mix(outer, inner, hot);
  float diskGlow = exp(-abs(diskR - 0.28) * 8.0) * (1.0 - smoothstep(0.52, 0.78, diskR));
  col += diskColor * (diskMask * doppler * 1.45 + diskGlow * 0.12);

  // Light from the rear disk is lensed into a crown and lower arc.
  float lensRadius = length(vec2(p.x, abs(p.y) * 1.12));
  float crown = exp(-abs(lensRadius - 0.185) * 82.0);
  crown *= smoothstep(0.025, 0.105, abs(p.y));
  float crownGrain = 0.62 + fbm(vec2(a * 4.0 - t * 2.2, r * 24.0)) * 0.58;
  col += crown * crownGrain * mix(vec3(0.95, 0.35, 0.055), vec3(1.0, 0.67, 0.22), hot) * 1.25;

  float photon = exp(-abs(r - 0.142) * 165.0);
  float bloom = exp(-abs(r - 0.153) * 31.0) * 0.30;
  col += (photon + bloom) * mix(vec3(0.94, 0.43, 0.11), vec3(1.0, 0.74, 0.34), u_light);

  // An uncompromising dark horizon is what gives the surrounding light scale.
  float outside = smoothstep(0.126, 0.139, r);
  col *= outside;
  col += exp(-r * 7.0) * outside * vec3(0.035, 0.018, 0.055);

  // The front image of the disk folds across the lower horizon as three soft arcs.
  float frontSpan = 1.0 - smoothstep(0.085, 0.126, abs(p.x));
  float frontCurve = p.y - 0.078 + p.x * p.x * 1.65;
  float frontArcA = exp(-abs(frontCurve) * 230.0);
  float frontArcB = exp(-abs(frontCurve + 0.020) * 245.0) * 0.64;
  float frontArcC = exp(-abs(frontCurve + 0.038) * 215.0) * 0.38;
  float frontFlicker = 0.82 + 0.18 * fbm(vec2(p.x * 18.0 - t * 3.2, 7.0));
  float frontMask = frontSpan * (1.0 - smoothstep(0.118, 0.139, r));
  vec3 frontColor = mix(vec3(0.90, 0.30, 0.045), vec3(1.0, 0.69, 0.24), u_light);
  col += frontColor * (frontArcA + frontArcB + frontArcC) * frontMask * frontFlicker * 0.92;

  float vignette = 1.0 - smoothstep(0.48, 1.12, length((uv - 0.5) * vec2(0.76, 1.0)));
  col *= 0.60 + vignette * 0.40;
  gl_FragColor = vec4(col, 1.0);
}`);

// ── moonclouds ───────────────────────────────────────────────────────
// Large moon, layered drifting cloud sea, soft moonlight.

const FRAG_MOONCLOUDS = frag(`#version 100
precision highp float;
varying vec2 v_uv;
uniform vec2 u_res;
uniform float u_time;
uniform float u_light;

float cloudBand(vec2 uv, float scale, float drift, float base, float softness) {
  vec2 q = uv * vec2(scale, scale * 1.25);
  q.x += u_time * drift;
  float body = fbm(q + fbm(q * 0.72 + 8.0) * 0.52);
  float envelope = smoothstep(base - 0.13, base + 0.15, uv.y);
  return smoothstep(0.49 - softness, 0.56 + softness, body) * envelope;
}

void main() {
  vec2 uv = vec2(v_uv.x, 1.0 - v_uv.y);
  float aspect = u_res.x / u_res.y;
  vec3 top = mix(vec3(0.006, 0.014, 0.050), vec3(0.58, 0.70, 0.86), u_light);
  vec3 horizon = mix(vec3(0.040, 0.065, 0.135), vec3(0.88, 0.84, 0.78), u_light);
  vec3 col = mix(top, horizon, smoothstep(0.0, 0.78, uv.y));

  // Quiet stars keep the open sky spacious.
  vec2 sg = uv * vec2(aspect, 1.0) * 62.0;
  vec2 scell = floor(sg);
  vec2 slocal = fract(sg) - 0.5;
  float sr = hash(scell);
  float stars = (1.0 - smoothstep(0.018, 0.055, length(slocal))) * step(0.955, sr);
  col += stars * mix(vec3(0.45, 0.58, 0.82), vec3(0.18, 0.28, 0.48), u_light) * mix(0.48, 0.14, u_light);

  vec2 moonCenter = vec2(0.73, 0.24);
  float moonRadius = 0.122;
  vec2 moonP = (uv - moonCenter) * vec2(aspect, 1.0) / moonRadius;
  float moonR = length(moonP);
  float moon = 1.0 - smoothstep(0.985, 1.015, moonR);
  float limb = sqrt(max(0.0, 1.0 - moonR * moonR));
  float maria = fbm(moonP * 3.2 + 7.0) * 0.26 + fbm(moonP * 8.5 + 13.0) * 0.09;
  float craters = softDisc(moonP - vec2(-0.28, -0.12), 0.16, 0.10) * 0.24;
  craters += softDisc(moonP - vec2(0.31, 0.18), 0.11, 0.07) * 0.18;
  craters += softDisc(moonP - vec2(0.07, -0.36), 0.08, 0.06) * 0.16;
  float directional = 0.68 + 0.32 * dot(normalize(vec3(moonP, max(limb, 0.01))), normalize(vec3(-0.45, -0.2, 1.0)));
  vec3 moonColor = mix(vec3(0.48, 0.53, 0.59), vec3(1.0, 0.91, 0.70), u_light);
  moonColor *= directional * (1.05 - maria - craters) + limb * 0.18;
  float moonGlow = exp(-max(0.0, moonR - 1.0) * 3.2) * (1.0 - moon);
  col += moonGlow * mix(vec3(0.10, 0.16, 0.28), vec3(0.42, 0.34, 0.20), u_light) * mix(0.70, 0.30, u_light);
  col = mix(col, moonColor, moon);

  // Distant ridges anchor the composition beneath the cloud sea.
  float ridge = 0.72 + sin(uv.x * 6.0 + 1.4) * 0.035 + sin(uv.x * 15.0) * 0.018;
  float peak = abs(fract(uv.x * 3.4 + 0.18) - 0.5);
  ridge -= max(0.0, 0.23 - peak) * 0.20;
  float ridgeMask = smoothstep(ridge - 0.006, ridge + 0.006, uv.y);
  col = mix(col, mix(vec3(0.012, 0.022, 0.052), vec3(0.30, 0.38, 0.49), u_light), ridgeMask * 0.88);

  float farNoise = fbm(uv * vec2(aspect * 2.1, 2.8) + vec2(u_time * 0.004, 3.0));
  float midNoise = fbm(uv * vec2(aspect * 3.4, 4.1) + vec2(u_time * 0.007, 9.0));
  float nearNoise = fbm(uv * vec2(aspect * 5.0, 5.8) + vec2(u_time * 0.011, 17.0));
  float cFar = smoothstep(0.44, 0.60, farNoise + smoothstep(0.42, 0.68, uv.y) * 0.24) * 0.54;
  float cMid = smoothstep(0.43, 0.59, midNoise + smoothstep(0.51, 0.76, uv.y) * 0.28) * 0.72;
  float cNear = smoothstep(0.42, 0.57, nearNoise + smoothstep(0.62, 0.86, uv.y) * 0.32);
  float cloud = clamp(cFar + cMid + cNear, 0.0, 1.0);
  float topNoise = fbm((uv - vec2(0.0, 0.014)) * vec2(aspect * 5.0, 5.8) + vec2(u_time * 0.011, 17.0));
  float cloudTop = smoothstep(0.42, 0.57, topNoise + smoothstep(0.62, 0.86, uv.y) * 0.32);
  float rim = clamp(cloudTop - cNear, 0.0, 1.0) * 2.2;
  float moonReach = exp(-abs(uv.x - moonCenter.x) * 2.2) * (1.0 - smoothstep(0.32, 0.96, uv.y));
  vec3 cloudDark = mix(vec3(0.055, 0.075, 0.125), vec3(0.44, 0.52, 0.62), u_light);
  vec3 cloudLit = mix(vec3(0.21, 0.25, 0.32), vec3(0.83, 0.82, 0.79), u_light);
  col = mix(col, mix(cloudDark, cloudLit, clamp(rim * (0.75 + moonReach), 0.0, 1.0)), cloud * 0.94);

  float vignette = 1.0 - smoothstep(0.48, 1.05, length((uv - 0.5) * vec2(0.78, 1.0)));
  col *= 0.64 + vignette * 0.36;
  gl_FragColor = vec4(col, 1.0);
}`);

// ── biolume ──────────────────────────────────────────────────────────
// Dark ocean depth, elegant jellyfish-like luminous forms and particles.

const FRAG_BIOLUME = frag(`#version 100
precision highp float;
varying vec2 v_uv;
uniform vec2 u_res;
uniform float u_time;
uniform float u_light;

vec3 jellyLight(vec2 p, vec2 center, float size, float phase, vec3 tint) {
  vec2 q = (p - center) / size;
  q.x += sin(u_time * 0.16 + phase + q.y * 0.9) * 0.055;
  float pulse = 0.94 + sin(u_time * 0.48 + phase) * 0.06;
  q.x /= pulse;

  float ellipse = length(vec2(q.x, q.y * 1.28));
  float dome = (1.0 - smoothstep(0.78, 1.04, ellipse))
    * smoothstep(0.15, -0.22, q.y) * smoothstep(-0.94, -0.62, q.y);
  float shell = exp(-abs(ellipse - 0.88) * 13.0)
    * smoothstep(0.20, -0.05, q.y) * smoothstep(-0.96, -0.62, q.y);
  float rim = exp(-abs(q.y + 0.02) * 22.0) * (1.0 - smoothstep(0.38, 0.92, abs(q.x)));
  float organs = exp(-length(vec2(q.x * 1.8, (q.y + 0.38) * 2.4)) * 3.8) * dome;

  float tentacles = 0.0;
  for (int k = 0; k < 5; k++) {
    float fk = float(k) - 2.0;
    float x0 = fk * 0.21;
    float wave = sin(q.y * (2.2 + abs(fk) * 0.25) + phase + fk) * (0.075 + abs(fk) * 0.015);
    float line = 1.0 - smoothstep(0.018, 0.052, abs(q.x - x0 - wave));
    float lengthFade = smoothstep(0.0, 0.15, q.y) * (1.0 - smoothstep(1.25 + abs(fk) * 0.16, 2.35, q.y));
    tentacles += line * lengthFade * (0.82 - abs(fk) * 0.09);
  }
  float halo = exp(-length(vec2(q.x, q.y * 0.85 + 0.16)) * 1.9) * 0.24;
  float light = dome * 0.48 + shell * 0.90 + rim * 0.72 + organs * 0.65 + tentacles * 0.52 + halo;
  return tint * light;
}

void main() {
  vec2 uv = vec2(v_uv.x, 1.0 - v_uv.y);
  float aspect = u_res.x / u_res.y;
  float t = u_time * 0.06;
  vec2 p = uv * vec2(aspect, 1.0);
  vec3 deep = mix(vec3(0.001, 0.012, 0.028), vec3(0.34, 0.64, 0.70), u_light);
  vec3 upper = mix(vec3(0.010, 0.058, 0.082), vec3(0.72, 0.90, 0.86), u_light);
  vec3 col = mix(upper, deep, smoothstep(0.0, 0.92, uv.y));
  col += fbm(vec2(p.x * 1.3, uv.y * 2.2 - t)) * vec3(0.006, 0.025, 0.034);

  float rays = 0.0;
  for (int r = 0; r < 6; r++) {
    float fr = float(r);
    float origin = 0.08 + fr * 0.22 + sin(t + fr * 2.7) * 0.035;
    float width = 0.025 + fr * 0.004;
    float rayX = p.x / aspect - origin - (uv.y * (0.06 + fr * 0.012));
    rays += exp(-abs(rayX) / width) * exp(-uv.y * 2.1) * 0.10;
  }
  col += rays * mix(vec3(0.025, 0.16, 0.20), vec3(0.08, 0.34, 0.36), u_light);

  vec3 aqua = mix(vec3(0.08, 0.64, 0.69), vec3(0.02, 0.48, 0.44), u_light);
  vec3 blue = mix(vec3(0.12, 0.36, 0.82), vec3(0.08, 0.28, 0.68), u_light);
  vec3 violet = mix(vec3(0.42, 0.18, 0.70), vec3(0.46, 0.18, 0.62), u_light);
  col += jellyLight(p, vec2(aspect * 0.23 + sin(t * 1.1) * 0.035, 0.29), 0.092, 2.0, blue) * 0.76;
  col += jellyLight(p, vec2(aspect * 0.69 + sin(t * 0.7 + 2.0) * 0.045, 0.38), 0.145, 5.0, aqua) * 1.12;
  col += jellyLight(p, vec2(aspect * 0.46 + sin(t * 0.9 + 4.0) * 0.025, 0.69), 0.082, 8.0, violet) * 0.62;
  col += jellyLight(p, vec2(aspect * 0.88 + sin(t * 0.8 + 7.0) * 0.032, 0.73), 0.105, 11.0, blue) * 0.74;
  col += jellyLight(p, vec2(aspect * 0.08 + sin(t + 1.0) * 0.018, 0.86), 0.062, 13.0, aqua) * 0.42;

  float particles = 0.0;
  for (int i = 0; i < 30; i++) {
    float seed = float(i) * 29.1 + 3.0;
    float px = hash11(seed) * aspect;
    float py = fract(hash11(seed + 1.0) + t * (0.025 + hash11(seed + 2.0) * 0.045));
    float size = 0.0012 + hash11(seed + 3.0) * 0.0038;
    float d = length(p - vec2(px, py));
    particles += (1.0 - smoothstep(size, size * 3.8, d)) * (0.25 + hash11(seed + 4.0) * 0.75);
  }
  col += particles * mix(vec3(0.08, 0.42, 0.42), vec3(0.02, 0.34, 0.31), u_light);

  float vignette = 1.0 - smoothstep(0.48, 1.05, length((uv - 0.5) * vec2(0.78, 1.0)));
  col *= 0.58 + vignette * 0.42;
  gl_FragColor = vec4(col, 1.0);
}`);


// ── dunes ────────────────────────────────────────────────────────────
// Layered dunes, warm grazing light, subtle wind-blown sand.

const FRAG_DUNES = frag(`#version 100
precision highp float;
varying vec2 v_uv;
uniform vec2 u_res;
uniform float u_time;
uniform float u_light;

float duneLine(float x, float seed, float scale) {
  float broad = sin(x * (1.18 + seed * 0.07) + seed * 2.1) * 0.060;
  broad += sin(x * (2.35 + seed * 0.04) - seed * 1.7) * 0.028;
  float wind = (noise(vec2(x * scale, seed * 3.7)) - 0.5) * 0.018;
  return broad + wind;
}

void main() {
  vec2 uv = vec2(v_uv.x, 1.0 - v_uv.y);
  float aspect = u_res.x / u_res.y;
  float t = u_time * 0.025;
  vec3 zenith = mix(vec3(0.055, 0.070, 0.145), vec3(0.58, 0.76, 0.91), u_light);
  vec3 dusk = mix(vec3(0.48, 0.20, 0.095), vec3(0.94, 0.68, 0.50), u_light);
  vec3 horizon = mix(vec3(0.92, 0.46, 0.16), vec3(1.0, 0.86, 0.65), u_light);
  vec3 col = mix(zenith, dusk, smoothstep(0.02, 0.42, uv.y));
  col = mix(col, horizon, smoothstep(0.28, 0.58, uv.y) * (1.0 - smoothstep(0.58, 0.78, uv.y)));

  vec2 sunP = (uv - vec2(0.72, 0.30)) * vec2(aspect, 1.0);
  float sunR = length(sunP);
  float sun = 1.0 - smoothstep(0.058, 0.064, sunR);
  float sunGlow = exp(-sunR * 4.8);
  col += sunGlow * mix(vec3(0.58, 0.20, 0.055), vec3(0.72, 0.38, 0.12), u_light) * 0.50;
  col = mix(col, mix(vec3(0.96, 0.62, 0.30), vec3(1.0, 0.91, 0.68), u_light), sun);

  // Five atmospheric layers create depth; each crest catches the same low sun.
  for (int d = 0; d < 5; d++) {
    float fd = float(d);
    float x = uv.x * aspect + t * (0.004 + fd * 0.001);
    float base = 0.47 + fd * 0.105;
    float amp = 1.20 + fd * 0.16;
    float y = base + duneLine(x, fd + 1.0, 3.0 + fd * 0.8) * amp;
    float yn = base + duneLine(x + 0.008, fd + 1.0, 3.0 + fd * 0.8) * amp;
    float slope = (yn - y) / 0.008;
    float light = 0.58 + clamp(-slope * 1.7, -0.24, 0.35);
    vec3 farSand = mix(vec3(0.48, 0.21, 0.115), vec3(0.88, 0.64, 0.38), u_light);
    vec3 nearSand = mix(vec3(0.18, 0.060, 0.035), vec3(0.58, 0.28, 0.13), u_light);
    vec3 sand = mix(farSand, nearSand, fd / 4.0) * light;
    float mask = smoothstep(y - 0.004, y + 0.004, uv.y);
    float crest = exp(-abs(uv.y - y) * (130.0 - fd * 9.0));
    float faceLight = 1.0 - smoothstep(0.0, 0.21, uv.y - y);
    sand *= 0.72 + faceLight * 0.34;
    col = mix(col, sand, mask);
    col += crest * mix(vec3(0.72, 0.31, 0.11), vec3(1.0, 0.76, 0.45), u_light) * (0.40 - fd * 0.032);
  }

  // Fine wind ripples live only on the foreground plane.
  float ripples = sin((uv.x * aspect * 13.0 + uv.y * 24.0) + fbm(uv * vec2(aspect * 4.0, 5.0)) * 2.2);
  ripples = pow(max(0.0, ripples * 0.5 + 0.5), 8.0) * smoothstep(0.72, 1.0, uv.y);
  col += ripples * mix(vec3(0.20, 0.085, 0.030), vec3(0.30, 0.13, 0.045), u_light) * 0.52;

  float sandAir = 0.0;
  for (int i = 0; i < 12; i++) {
    float seed = float(i) * 17.7;
    float px = fract(hash11(seed) + t * (0.018 + hash11(seed + 1.0) * 0.035));
    float py = 0.48 + hash11(seed + 2.0) * 0.36;
    sandAir += exp(-abs(uv.y - py) * 90.0) * exp(-abs(uv.x - px) * 16.0) * 0.045;
  }
  col += sandAir * mix(vec3(0.55, 0.28, 0.10), vec3(0.68, 0.42, 0.18), u_light);
  float vignette = 1.0 - smoothstep(0.52, 1.10, length((uv - 0.5) * vec2(0.76, 1.0)));
  col *= 0.68 + vignette * 0.32;
  gl_FragColor = vec4(col, 1.0);
}`);


// ── scene registry ──────────────────────────────────────────────────

const SCENES: Record<SceneName, SceneDef> = {
  waves: { fragmentShader: FRAG_WAVES },
  aurora: { fragmentShader: FRAG_AURORA },
  nebula: { fragmentShader: FRAG_NEBULA },
  starfield: { fragmentShader: FRAG_STARFIELD },
  blackhole: { fragmentShader: FRAG_BLACKHOLE },
  moonclouds: { fragmentShader: FRAG_MOONCLOUDS },
  biolume: { fragmentShader: FRAG_BIOLUME },
  dunes: { fragmentShader: FRAG_DUNES },
};

// ── shared engine constants ─────────────────────────────────────────

const VERTICES = new Float32Array([-1, -1, 3, -1, -1, 3]);
const MAX_DPR = 1.5;
const TARGET_FPS = 10;
const FRAME_MS = 1000 / TARGET_FPS;

// ── WebGL helpers ───────────────────────────────────────────────────

function compileShader(gl: WebGLRenderingContext, type: number, source: string): WebGLShader {
  const shader = gl.createShader(type);
  if (!shader) throw new Error("WebGL createShader returned null");
  gl.shaderSource(shader, source);
  gl.compileShader(shader);
  if (!gl.getShaderParameter(shader, gl.COMPILE_STATUS)) {
    const log = gl.getShaderInfoLog(shader) ?? "unknown compile error";
    gl.deleteShader(shader);
    throw new Error(`Shader compile failed: ${log}`);
  }
  return shader;
}

function linkProgram(gl: WebGLRenderingContext, vert: WebGLShader, frag: WebGLShader): WebGLProgram {
  const prog = gl.createProgram();
  if (!prog) throw new Error("WebGL createProgram returned null");
  gl.attachShader(prog, vert);
  gl.attachShader(prog, frag);
  gl.linkProgram(prog);
  if (!gl.getProgramParameter(prog, gl.LINK_STATUS)) {
    const log = gl.getProgramInfoLog(prog) ?? "unknown link error";
    gl.deleteProgram(prog);
    throw new Error(`Program link failed: ${log}`);
  }
  return prog;
}

// ── component ───────────────────────────────────────────────────────

interface DynamicWallpaperProps {
  scene: SceneName;
  animate?: boolean;
}

export function DynamicWallpaper({ scene, animate = true }: DynamicWallpaperProps) {
  const canvasRef = useRef<HTMLCanvasElement>(null);

  // Re-initialise WebGL when the scene changes (shader source differs).
  useEffect(() => {
    const canvas = canvasRef.current;
    if (!canvas) return;

    const def = SCENES[scene];
    if (!def) return;

    let rafId = 0;
    let running = true;
    let lastFrame = 0;
    let gl: WebGLRenderingContext | null = null;
    let program: WebGLProgram | null = null;
    let buffer: WebGLBuffer | null = null;
    let vertShader: WebGLShader | null = null;
    let fragShader: WebGLShader | null = null;
    let timeUniform: WebGLUniformLocation | null = null;
    let resUniform: WebGLUniformLocation | null = null;
    let lightUniform: WebGLUniformLocation | null = null;
    let animTime = animate ? 0 : 12;
    let observer: ResizeObserver | null = null;

    // ── prefers-reduced-motion ────────────────────────────────────
    const mql = typeof window.matchMedia === "function"
      ? window.matchMedia("(prefers-reduced-motion: reduce)")
      : null;
    const colorMql = typeof window.matchMedia === "function"
      ? window.matchMedia("(prefers-color-scheme: light)")
      : null;
    const isLight = () => {
      const theme = document.documentElement.dataset.theme;
      return theme === "light" || (theme !== "dark" && (colorMql?.matches ?? false));
    };
    let reducedMotion = mql?.matches ?? false;
    const onMotionChange = (e: MediaQueryListEvent) => {
      reducedMotion = e.matches;
      if (reducedMotion) {
        cancelRAF();
        renderOneFrame(animTime);
      } else if (animate) {
        lastFrame = 0;
        if (document.visibilityState !== "hidden") scheduleRAF();
      }
    };
    mql?.addEventListener("change", onMotionChange);
    const onThemeChange = () => {
      if (gl && program) gl.uniform1f(lightUniform, isLight() ? 1 : 0);
      renderOneFrame(animTime);
    };
    colorMql?.addEventListener("change", onThemeChange);
    const themeObserver = typeof MutationObserver !== "undefined"
      ? new MutationObserver(onThemeChange)
      : null;
    themeObserver?.observe(document.documentElement, { attributes: true, attributeFilter: ["data-theme"] });

    // ── WebGL init ────────────────────────────────────────────────
    function init(): boolean {
      gl = canvas!.getContext("webgl", {
        alpha: true,
        antialias: false,
        depth: false,
        stencil: false,
        premultipliedAlpha: false,
        preserveDrawingBuffer: false,
      });
      if (!gl) return false;

      try {
        vertShader = compileShader(gl, gl.VERTEX_SHADER, VERT);
        fragShader = compileShader(gl, gl.FRAGMENT_SHADER, def.fragmentShader);
        program = linkProgram(gl, vertShader, fragShader);
        gl.deleteShader(vertShader);
        gl.deleteShader(fragShader);
        vertShader = null;
        fragShader = null;

        buffer = gl.createBuffer();
        if (!buffer) throw new Error("WebGL createBuffer returned null");
        gl.bindBuffer(gl.ARRAY_BUFFER, buffer);
        gl.bufferData(gl.ARRAY_BUFFER, VERTICES, gl.STATIC_DRAW);

        const aPos = gl.getAttribLocation(program, "a_pos");
        gl.enableVertexAttribArray(aPos);
        gl.vertexAttribPointer(aPos, 2, gl.FLOAT, false, 0, 0);

        gl.useProgram(program);
        timeUniform = gl.getUniformLocation(program, "u_time");
        resUniform = gl.getUniformLocation(program, "u_res");
        lightUniform = gl.getUniformLocation(program, "u_light");
        gl.uniform1f(lightUniform, isLight() ? 1 : 0);
        resize();
        return true;
      } catch (error) {
        console.warn(`[DynamicWallpaper:${scene}] WebGL initialization failed`, error);
        releaseGL();
        return false;
      }
    }

    function releaseGL() {
      if (!gl) return;
      try {
        if (buffer) gl.deleteBuffer(buffer);
        if (program) gl.deleteProgram(program);
        if (vertShader) gl.deleteShader(vertShader);
        if (fragShader) gl.deleteShader(fragShader);
      } catch { /* best effort */ }
      forgetGL();
    }

    function forgetGL() {
      buffer = null;
      vertShader = null;
      fragShader = null;
      timeUniform = null;
      resUniform = null;
      lightUniform = null;
      gl = null;
      program = null;
    }

    // ── resize ────────────────────────────────────────────────────
    function resize() {
      if (!gl || !canvas) return;
      const bounds = canvas.getBoundingClientRect();
      const w = Math.round(bounds.width);
      const h = Math.round(bounds.height);
      if (w === 0 || h === 0) return;
      const dpr = Math.min(window.devicePixelRatio || 1, MAX_DPR);
      const pixelWidth = Math.max(1, Math.round(w * dpr));
      const pixelHeight = Math.max(1, Math.round(h * dpr));
      if (canvas.width !== pixelWidth) canvas.width = pixelWidth;
      if (canvas.height !== pixelHeight) canvas.height = pixelHeight;
      gl.viewport(0, 0, canvas.width, canvas.height);
      if (program) {
        gl.useProgram(program);
        gl.uniform2f(resUniform, w, h);
      }
    }

    if (typeof ResizeObserver !== "undefined") {
      observer = new ResizeObserver(() => {
        resize();
        if (!animate) renderOneFrame(animTime);
      });
      observer.observe(canvas);
    }

    // ── render ────────────────────────────────────────────────────
    function render(now: number) {
      rafId = 0;
      if (!running) return;
      if (!gl || !program) {
        if (init()) {
          renderOneFrame(animTime);
        }
        return;
      }

      if (reducedMotion) return;

      if (lastFrame === 0) lastFrame = now - FRAME_MS;
      const elapsed = now - lastFrame;
      if (elapsed < FRAME_MS - 0.5) {
        rafId = requestAnimationFrame(render);
        return;
      }
      lastFrame = now - (elapsed % FRAME_MS);

      animTime += Math.min(elapsed, 100) * 0.001;
      renderOneFrame(animTime);
      rafId = requestAnimationFrame(render);
    }

    function renderOneFrame(time: number) {
      if (!gl || !program) return;
      try {
        gl.useProgram(program);
        gl.uniform1f(timeUniform, time);
        gl.drawArrays(gl.TRIANGLES, 0, 3);
      } catch { /* ignore draw errors */ }
    }

    function cancelRAF() {
      if (rafId) {
        cancelAnimationFrame(rafId);
        rafId = 0;
      }
      lastFrame = 0;
    }

    function scheduleRAF() {
      cancelRAF();
      rafId = requestAnimationFrame(render);
    }

    // ── visibility ────────────────────────────────────────────────
    function onVisibility() {
      if (document.visibilityState === "hidden") {
        cancelRAF();
      } else if (animate && !reducedMotion) {
        lastFrame = 0;
        scheduleRAF();
      }
    }
    document.addEventListener("visibilitychange", onVisibility);

    // ── context loss ──────────────────────────────────────────────
    function onContextLost(e: Event) {
      e.preventDefault();
      cancelRAF();
      forgetGL();
    }
    function onContextRestored() {
      if (!running) return;
      cancelRAF();
      if (init()) {
        resize();
        renderOneFrame(animTime);
        if (animate && !reducedMotion && document.visibilityState !== "hidden") scheduleRAF();
      }
    }
    canvas.addEventListener("webglcontextlost", onContextLost);
    canvas.addEventListener("webglcontextrestored", onContextRestored);

    // ── startup ───────────────────────────────────────────────────
    if (init()) {
      renderOneFrame(animTime);
      if (animate && !reducedMotion && document.visibilityState !== "hidden") scheduleRAF();
    }

    // ── cleanup ───────────────────────────────────────────────────
    return () => {
      running = false;
      cancelRAF();
      document.removeEventListener("visibilitychange", onVisibility);
      mql?.removeEventListener("change", onMotionChange);
      colorMql?.removeEventListener("change", onThemeChange);
      themeObserver?.disconnect();
      canvas.removeEventListener("webglcontextlost", onContextLost);
      canvas.removeEventListener("webglcontextrestored", onContextRestored);
      if (observer) {
        observer.disconnect();
        observer = null;
      }
      releaseGL();
    };
  }, [animate, scene]);

  return (
    <canvas
      ref={canvasRef}
      className="session-background__dynamic"
      aria-hidden="true"
    />
  );
}
