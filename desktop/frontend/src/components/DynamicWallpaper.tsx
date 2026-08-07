import { useEffect, useRef } from "react";

// ── scene definitions ──────────────────────────────────────────────
// Each scene supplies only its fragment shader; the shared engine
// owns the vertex shader, RAF loop, resize, context-loss, theme, and
// reduced-motion lifecycle.

export const DYNAMIC_WALLPAPER_SCENES = [
  "waves",
  "aurora",
  "nebula",
  "embers",
  "starfield",
  "blackhole",
  "moonclouds",
  "biolume",
  "silk",
  "dunes",
  "raincity",
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

  float horizon = 0.28;
  float t = u_time * 0.12;

  float depth = smoothstep(horizon, 1.0, uv.y);

  vec2 wc = vec2(uv.x * aspect, uv.y) * 1.6;

  float swell  = sin(wc.x * 1.8 + t * 0.6 + sin(wc.y * 0.9 + t * 0.25) * 1.4);
  swell += sin(wc.x * 2.5 - t * 0.45 + 1.7) * 0.6;

  float wave = fbm(wc * vec2(2.2, 1.4) + vec2(t * 0.55, t * 0.35));

  float ripple  = sin(wc.x * 13.0 + t * 1.1 + wc.y * 2.5) * 0.5 + 0.5;
  ripple += sin(wc.x * 9.0 - t * 0.85 - wc.y * 3.2) * 0.5 + 0.5;

  float height = swell * 0.35 + (wave - 0.5) * 1.2 + (ripple - 0.5) * 0.3;

  float scale = 0.18 + depth * 0.82;
  height *= scale;

  float crestSoft = smoothstep(0.03, 0.0, abs(height - 0.12 * scale));
  float crestHard = smoothstep(0.008, 0.0, abs(height - 0.18 * scale));

  vec3 deep     = mix(vec3(0.015, 0.06, 0.18), vec3(0.035, 0.16, 0.30), u_light);
  vec3 mid      = mix(vec3(0.03, 0.12, 0.28), vec3(0.06, 0.26, 0.43), u_light);
  vec3 shelf    = mix(vec3(0.06, 0.20, 0.38), vec3(0.12, 0.38, 0.55), u_light);
  vec3 horizonC = mix(vec3(0.12, 0.28, 0.45), vec3(0.34, 0.58, 0.70), u_light);

  vec3 water = horizonC;
  water = mix(water, shelf, smoothstep(0.0, 0.35, depth));
  water = mix(water, mid, smoothstep(0.25, 0.68, depth));
  water = mix(water, deep, smoothstep(0.58, 1.0, depth));

  float shade = height * 0.45 + 0.55;
  water *= 0.75 + 0.25 * shade;

  vec3 crestClr = mix(vec3(0.35, 0.60, 0.80), vec3(0.72, 0.88, 0.92), u_light);
  water = mix(water, crestClr, crestSoft * 0.10 * (0.5 + 0.5 * depth));
  water = mix(water, crestClr * 1.4, crestHard * 0.06);

  float spec = pow(crestHard, 3.5) * 0.35 + pow(crestSoft, 6.0) * 0.12;
  water += spec * vec3(0.45, 0.55, 0.65) * (0.4 + 0.6 * depth);

  float haze = 1.0 - smoothstep(horizon, horizon + 0.18, uv.y);
  water = mix(water, mix(vec3(0.22, 0.38, 0.55), vec3(0.58, 0.72, 0.78), u_light), haze * 0.45);

  float sunX = 0.62;
  float sun = exp(-abs(uv.x - sunX) * 9.0) * exp(-abs(uv.y - horizon) * 16.0);
  water += sun * vec3(0.25, 0.22, 0.12) * 0.18;

  if (uv.y < horizon) {
    vec3 skyTop    = mix(vec3(0.035, 0.09, 0.24), vec3(0.32, 0.52, 0.68), u_light);
    vec3 skyHor    = mix(vec3(0.14, 0.28, 0.48), vec3(0.68, 0.78, 0.82), u_light);
    float skyGrad  = uv.y / horizon;
    vec3 sky       = mix(skyTop, skyHor, skyGrad);
    sky += sun * vec3(0.35, 0.3, 0.18) * 0.35;
    water = sky;
  }

  float v = 1.0 - length((uv - 0.5) * vec2(1.0, 0.65)) * 0.25;
  water *= v;

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

  vec3 skyTop    = mix(vec3(0.01, 0.02, 0.08), vec3(0.04, 0.08, 0.18), u_light);
  vec3 skyHor    = mix(vec3(0.02, 0.04, 0.12), vec3(0.06, 0.10, 0.22), u_light);
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
  vec3 starColor = mix(vec3(0.6, 0.7, 0.9), vec3(0.8, 0.85, 0.95), u_light);
  sky += starField * starColor * 0.6;

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
    if (band == 0) bandColor = vec3(0.15, 0.72, 0.45);
    else if (band == 1) bandColor = vec3(0.10, 0.60, 0.75);
    else if (band == 2) bandColor = vec3(0.35, 0.30, 0.75);
    else bandColor = vec3(0.12, 0.68, 0.50);

    bandColor = mix(bandColor, bandColor * 1.3, u_light);
    aurora += curtain * bandColor * 0.55;
  }

  float horizonGlow = exp(-abs(uv.y - 0.02) * 6.0);
  vec3 glowColor = mix(vec3(0.08, 0.25, 0.35), vec3(0.18, 0.40, 0.50), u_light);
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

  vec3 bg = mix(vec3(0.008, 0.006, 0.025), vec3(0.025, 0.018, 0.06), u_light);

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
  vec3 starColor = mix(vec3(0.7, 0.75, 0.9), vec3(0.85, 0.88, 0.95), u_light);
  vec3 color = bg + stars * starColor * 0.5;

  vec2 nc1 = uv * vec2(aspect * 1.8, 1.8) + vec2(t * 0.08, t * 0.04);
  float cloud1 = fbm(nc1);
  cloud1 = smoothstep(0.35, 0.7, cloud1);

  vec2 nc2 = uv * vec2(aspect * 3.0, 3.0) + vec2(t * 0.12, -t * 0.06) + 5.0;
  float cloud2 = fbm(nc2);
  cloud2 = smoothstep(0.4, 0.65, cloud2);

  vec2 nc3 = uv * vec2(aspect * 5.5, 5.5) + vec2(-t * 0.15, t * 0.1) + 12.0;
  float cloud3 = fbm(nc3);
  cloud3 *= 0.5;

  vec3 nebulaColor1 = mix(vec3(0.25, 0.08, 0.35), vec3(0.30, 0.12, 0.40), u_light);
  vec3 nebulaColor2 = mix(vec3(0.05, 0.18, 0.45), vec3(0.08, 0.25, 0.50), u_light);
  vec3 nebulaColor3 = mix(vec3(0.08, 0.30, 0.35), vec3(0.12, 0.35, 0.40), u_light);

  color += cloud1 * nebulaColor1 * 0.35;
  color += cloud2 * nebulaColor2 * 0.30;
  color += cloud3 * nebulaColor3 * 0.20;

  float vignette = 1.0 - length((uv - 0.5) * vec2(1.0, 0.7)) * 0.35;
  color *= vignette;

  gl_FragColor = vec4(color, 1.0);
}`);

// ── embers (rebuilt) ────────────────────────────────────────────────
// Cinematic low-key firelight with varied rising embers.
// Continuous hash-based placement avoids uniform dot grids.

const FRAG_EMBERS = frag(`#version 100
precision highp float;
varying vec2 v_uv;
uniform vec2 u_res;
uniform float u_time;
uniform float u_light;

float hash1(float n) {
  return fract(sin(n) * 43758.5453);
}

void main() {
  vec2 uv = vec2(v_uv.x, 1.0 - v_uv.y);
  float aspect = u_res.x / u_res.y;
  float t = u_time * 0.3;

  // ── deep atmospheric background ─────────────────────────────────
  vec3 bg = mix(
    mix(vec3(0.03, 0.025, 0.03), vec3(0.07, 0.06, 0.07), u_light),
    mix(vec3(0.05, 0.03, 0.02), vec3(0.10, 0.07, 0.05), u_light),
    uv.y
  );

  // drifting smoke
  float smoke = fbm(uv * vec2(aspect * 2.8, 2.8) + vec2(t * 0.08, t * 0.05));
  bg += smoke * mix(vec3(0.012, 0.01, 0.008), vec3(0.025, 0.02, 0.016), u_light);

  // ── low warm glow ──────────────────────────────────────────────
  float glowDist = length((uv - vec2(0.5, 0.04)) * vec2(1.0, 2.0));
  float glow = exp(-glowDist * 2.2) * 0.38;
  vec3 glowColor = mix(vec3(0.50, 0.20, 0.05), vec3(0.60, 0.32, 0.13), u_light);
  bg += glow * glowColor;

  // secondary cooler glow higher up
  float glow2 = exp(-length((uv - vec2(0.5, 0.35)) * vec2(1.4, 1.0)) * 3.5) * 0.08;
  bg += glow2 * mix(vec3(0.08, 0.06, 0.15), vec3(0.14, 0.11, 0.22), u_light);

  // ── varied embers (3 tiers × 12 embers each) ───────────────────
  float emberCore = 0.0;
  float emberHalo = 0.0;

  for (int tier = 0; tier < 3; tier++) {
    float tierF = float(tier);
    float baseSize = 0.0035 + tierF * 0.0015;
    float baseSpeed = 0.22 + tierF * 0.12;
    float brightnessScale = 1.0 - tierF * 0.18;

    for (int i = 0; i < 12; i++) {
      float idx = tierF * 12.0 + float(i);
      float seed = idx * 73.0 + 19.0;

      // unique position via hash
      float px = 0.05 + hash1(seed + 0.3) * 0.90;
      float pyOff = hash1(seed + 0.7);

      // vertical cycle
      float speed = baseSpeed * (0.8 + 0.4 * hash1(seed + 1.1));
      float yCycle = fract(pyOff + t * speed);

      // sinusoidal horizontal drift
      float driftAmp = 0.03 + 0.04 * hash1(seed + 1.5);
      float driftFreq = 0.7 + 0.5 * hash1(seed + 1.9);
      float driftX = sin(t * driftFreq + seed) * driftAmp;

      float ex = px + driftX;
      float ey = yCycle;

      // size variation
      float size = baseSize * (0.7 + 0.6 * hash1(seed + 0.5));

      // fade near top and bottom
      float fade = 1.0 - smoothstep(0.85, 1.0, yCycle);
      fade *= smoothstep(0.0, 0.06, yCycle);

      // distance from ember center
      vec2 ed = (uv - vec2(ex, ey)) * vec2(aspect, 0.42);
      float dist = length(ed / size);

      // core: tight bright point
      float core = (1.0 - smoothstep(0.0, 1.1, dist)) * fade * brightnessScale;

      // halo: softer surrounding glow
      float halo = (1.0 - smoothstep(0.0, 3.4, dist)) * fade * brightnessScale * 0.30;

      emberCore += core;
      emberHalo += halo;
    }
  }

  // ember colours: warm orange/amber
  vec3 emberColor = mix(vec3(1.0, 0.45, 0.08), vec3(0.95, 0.55, 0.22), u_light);
  bg += emberCore * emberColor * 1.45;
  bg += emberHalo * vec3(0.55, 0.16, 0.025) * 0.85;

  // vignette
  float vignette = 1.0 - length((uv - 0.5) * vec2(1.0, 0.7)) * 0.45;
  bg *= vignette;

  gl_FragColor = vec4(bg, 1.0);
}`);

// ── starfield ───────────────────────────────────────────────────────
// Deep parallax star field, Milky Way haze, sparse restrained meteors.

const FRAG_STARFIELD = frag(`#version 100
precision highp float;
varying vec2 v_uv;
uniform vec2 u_res;
uniform float u_time;
uniform float u_light;

float hash1f(float n) {
  return fract(sin(n) * 43758.5453);
}

void main() {
  vec2 uv = vec2(v_uv.x, 1.0 - v_uv.y);
  float aspect = u_res.x / u_res.y;
  float t = u_time * 0.04;

  // deep space background
  vec3 bg = mix(vec3(0.003, 0.005, 0.020), vec3(0.010, 0.014, 0.045), u_light);

  // ── 4 parallax star layers ─────────────────────────────────────
  float stars = 0.0;
  for (int L = 0; L < 4; L++) {
    float depth = float(L) + 1.0;
    float px = 1.0 / depth;
    float sc = 24.0 + float(L) * 20.0;
    vec2 suv = uv * vec2(aspect, 1.0) * sc;
    suv += vec2(t * px * 0.22, t * px * 0.09) + float(L) * 11.0;
    vec2 cell = floor(suv);
    vec2 lc = fract(suv) - 0.5;
    float r = hash(cell + float(L) * 37.0);
    float tw = 0.55 + 0.45 * sin(t * 2.2 + r * 50.0 + float(L) * 9.0);
    float mag = 0.035 + 0.012 * float(L);
    stars += (1.0 - smoothstep(0.0, mag, length(lc))) * step(0.895, r) * tw * (1.0 - float(L) * 0.10);
  }
  vec3 sc = mix(vec3(0.65, 0.72, 0.88), vec3(0.82, 0.86, 0.94), u_light);
  vec3 col = bg + stars * sc * 2.8;

  // ── Milky Way band ─────────────────────────────────────────────
  float mwDist = abs(uv.y - 0.48 + (uv.x - 0.5) * 0.30 - sin(uv.x * 2.2) * 0.025);
  float mw = exp(-mwDist * 5.0) * 0.28;
  mw += exp(-mwDist * 18.0) * 0.10;
  float mwDetail = fbm(uv * vec2(aspect * 3.5, 1.8) + t * 0.02) * 0.5 + 0.5;
  mw *= 0.65 + 0.35 * mwDetail;
  vec3 mwColor = mix(vec3(0.24, 0.16, 0.42), vec3(0.34, 0.28, 0.52), u_light);
  col += mw * mwColor * 2.6;
  col += fbm(uv * vec2(aspect * 2.2, 2.2) + vec2(t * 0.025, -t * 0.015))
    * mix(vec3(0.035, 0.025, 0.085), vec3(0.055, 0.045, 0.11), u_light);

  // ── sparse meteors ─────────────────────────────────────────────
  float meteorSum = 0.0;
  for (int m = 0; m < 3; m++) {
    float seed = float(m) * 141.0 + 59.0;
    float cycle = fract(t * 0.055 + seed * 0.07);
    float appear = smoothstep(0.0, 0.04, cycle) * (1.0 - smoothstep(0.22, 0.30, cycle));
    float mx = hash1f(seed + 0.1) * 0.65 + 0.18;
    float my = hash1f(seed + 0.2) * 0.55 + 0.15;
    float angle = (hash1f(seed + 0.3) - 0.5) * 0.5 - 0.45;
    float len = 0.06 + hash1f(seed + 0.4) * 0.05;
    vec2 dir = vec2(cos(angle), sin(angle));
    float along = dot(uv - vec2(mx, my), dir);
    float across = abs(dot(uv - vec2(mx, my), vec2(-dir.y, dir.x)));
    float streak = exp(-across * 55.0) * exp(-max(0.0, -along) * 14.0) * step(0.0, along) * step(along, len);
    meteorSum += streak * appear;
  }
  col += meteorSum * vec3(0.65, 0.75, 1.0) * 0.5;

  // vignette
  col *= 1.0 - length((uv - 0.5) * vec2(1.0, 0.7)) * 0.28;

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

void main() {
  vec2 uv = vec2(v_uv.x, 1.0 - v_uv.y);
  float aspect = u_res.x / u_res.y;
  float t = u_time * 0.06;

  vec2 center = vec2(0.62, 0.42);
  vec2 dp = (uv - center) * vec2(aspect, 1.0);
  float dist = length(dp);
  float angle = atan(dp.y, dp.x);

  // gravitational lens warp: stars bend around center
  float lensStrength = 0.055 / (dist + 0.08);
  float warp = lensStrength * smoothstep(0.15, 0.05, dist);

  // ── lensed star field ──────────────────────────────────────────
  vec3 bg = mix(vec3(0.003, 0.004, 0.018), vec3(0.008, 0.012, 0.040), u_light);
  float stars = 0.0;
  for (int L = 0; L < 4; L++) {
    float depth = float(L) + 1.0;
    float px = 1.0 / depth;
    float sc = 22.0 + float(L) * 16.0;

    // warp star coords
    float wd = max(dist, 0.02);
    float wa = angle + warp * px + t * 0.15 * px;

    vec2 suv = vec2(center.x + cos(wa) * wd / aspect, center.y + sin(wa) * wd) * sc;
    suv += float(L) * 13.0 + t * px * 0.1;

    vec2 cell = floor(suv);
    vec2 lc = fract(suv) - 0.5;
    float r = hash(cell + float(L) * 41.0);
    float tw = 0.5 + 0.5 * sin(t * 2.5 + r * 45.0 + float(L) * 8.0);
    stars += smoothstep(0.07, 0.0, length(lc)) * step(0.955, r) * tw * (1.0 - float(L) * 0.1);
  }
  vec3 sc = mix(vec3(0.7, 0.75, 0.9), vec3(0.85, 0.88, 0.95), u_light);
  vec3 col = bg + stars * sc * 0.75;

  // ── accretion disk ─────────────────────────────────────────────
  vec2 diskP = vec2(dp.x, dp.y * 3.4);
  float diskDist = length(diskP);
  float diskAngle = atan(diskP.y, diskP.x) + t * 0.7;
  float disk = exp(-abs(diskDist - 0.17) * 38.0);

  // disk colour gradient: inner hot white-blue → outer orange
  float diskGrad = smoothstep(0.10, 0.27, diskDist);
  vec3 diskInner = mix(vec3(0.6, 0.75, 1.0), vec3(0.7, 0.85, 1.0), u_light);
  vec3 diskOuter = mix(vec3(0.85, 0.40, 0.08), vec3(1.0, 0.55, 0.18), u_light);
  vec3 diskColor = mix(diskInner, diskOuter, diskGrad);

  // Doppler brightening on one side
  float doppler = 0.65 + 0.35 * cos(diskAngle + 1.2);
  float diskGrain = 0.78 + noise(vec2(diskAngle * 4.0, t * 1.8)) * 0.45;
  col += disk * diskColor * doppler * diskGrain * 1.25;

  // ── photon ring (bright thin ring just outside horizon) ────────
  float photonRing = exp(-abs(dist - 0.085) * 95.0) * 0.72;
  col += photonRing * mix(vec3(0.35, 0.65, 1.0), vec3(0.55, 0.82, 1.0), u_light) * 0.78;

  // ── event horizon ──────────────────────────────────────────────
  float horizon = smoothstep(0.055, 0.07, dist);
  col *= horizon;

  // subtle glow around horizon
  float horizonGlow = exp(-dist * 12.0) * 0.12;
  col += horizonGlow * mix(vec3(0.08, 0.06, 0.18), vec3(0.15, 0.12, 0.28), u_light);

  // ── vignette ───────────────────────────────────────────────────
  col *= 1.0 - length((uv - 0.5) * vec2(1.0, 0.7)) * 0.35;

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

void main() {
  vec2 uv = vec2(v_uv.x, 1.0 - v_uv.y);
  float aspect = u_res.x / u_res.y;
  float t = u_time * 0.06;

  // ── night sky ──────────────────────────────────────────────────
  vec3 skyTop = mix(vec3(0.02, 0.03, 0.12), vec3(0.05, 0.07, 0.22), u_light);
  vec3 skyHor = mix(vec3(0.04, 0.06, 0.18), vec3(0.10, 0.14, 0.30), u_light);
  vec3 sky = mix(skyTop, skyHor, smoothstep(0.0, 0.6, uv.y));

  // faint stars
  float stars = 0.0;
  for (int i = 0; i < 2; i++) {
    float sc = 38.0 + float(i) * 25.0;
    vec2 suv = uv * vec2(aspect, 1.0) * sc + float(i) * 17.0;
    vec2 cell = floor(suv);
    vec2 lc = fract(suv) - 0.5;
    float r = hash(cell + float(i) * 29.0);
    stars += smoothstep(0.08, 0.0, length(lc)) * step(0.96, r) * 0.7;
  }
  sky += stars * mix(vec3(0.6, 0.65, 0.8), vec3(0.8, 0.83, 0.9), u_light) * 0.3;

  vec3 col = sky;

  // ── moon ───────────────────────────────────────────────────────
  vec2 moonCenter = vec2(0.72, 0.19);
  float moonDist = length((uv - moonCenter) * vec2(aspect, 1.0));
  float moonRadius = 0.085;

  // moon surface
  float moonBody = 1.0 - smoothstep(moonRadius - 0.003, moonRadius + 0.003, moonDist);

  // subtle surface detail
  vec2 moonUV = (uv - moonCenter) * vec2(aspect, 1.0) / moonRadius;
  float surface = fbm(moonUV * 4.0 + 3.0) * 0.16;
  surface += fbm(moonUV * 10.0 + 7.0) * 0.07;
  float limb = sqrt(max(0.0, 1.0 - dot(moonUV, moonUV)));

  vec3 moonDark = mix(vec3(0.75, 0.72, 0.68), vec3(0.85, 0.83, 0.78), u_light);
  vec3 moonColor = moonDark * (0.62 + limb * 0.48) - surface;
  col = mix(col, moonColor, moonBody);

  // ── moonlight glow ─────────────────────────────────────────────
  float glow = exp(-moonDist * 6.0) * 0.24;
  glow += exp(-moonDist * 16.0) * 0.14;
  vec3 glowColor = mix(vec3(0.30, 0.35, 0.55), vec3(0.45, 0.50, 0.65), u_light);
  col += glow * glowColor;

  // ── cloud layers ───────────────────────────────────────────────
  float horizon = 0.38;
  float cloudMask = smoothstep(horizon, horizon + 0.25, uv.y);

  // layer 1: distant thin clouds
  float c1 = fbm(uv * vec2(aspect * 2.0, 1.5) + vec2(t * 0.15, t * 0.05));
  c1 = smoothstep(0.42, 0.58, c1) * 0.25 * cloudMask;

  // layer 2: mid-level clouds
  float c2 = fbm(uv * vec2(aspect * 3.5, 2.2) + vec2(t * 0.22, t * 0.08) + 8.0);
  c2 = smoothstep(0.38, 0.55, c2) * 0.30 * cloudMask;

  // layer 3: foreground clouds
  float c3 = fbm(uv * vec2(aspect * 5.0, 3.0) + vec2(t * 0.30, t * 0.12) + 15.0);
  c3 = smoothstep(0.44, 0.60, c3) * 0.28 * cloudMask;

  vec3 cloudColor = mix(vec3(0.30, 0.34, 0.52), vec3(0.60, 0.64, 0.76), u_light);
  col += (c1 + c2 + c3) * cloudColor;

  // moon illuminates clouds from above
  float moonIllum = smoothstep(horizon, horizon + 0.1, uv.y) * (1.0 - smoothstep(0.0, 0.6, uv.y));
  col += moonIllum * glowColor * 0.06;

  // vignette
  col *= 1.0 - length((uv - 0.5) * vec2(1.0, 0.7)) * 0.25;

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

float hash1f(float n) {
  return fract(sin(n) * 43758.5453);
}

void main() {
  vec2 uv = vec2(v_uv.x, 1.0 - v_uv.y);
  float aspect = u_res.x / u_res.y;
  float t = u_time * 0.15;

  // ── deep ocean gradient ────────────────────────────────────────
  vec3 deepColor = mix(vec3(0.005, 0.008, 0.040), vec3(0.012, 0.020, 0.070), u_light);
  vec3 midColor = mix(vec3(0.010, 0.018, 0.060), vec3(0.025, 0.038, 0.095), u_light);
  vec3 col = mix(deepColor, midColor, uv.y);

  // ── faint caustic light rays from above ────────────────────────
  float caustic = 0.0;
  for (int r = 0; r < 5; r++) {
    float rx = 0.1 + float(r) * 0.2;
    float rAngle = sin(rx * 12.0 + t * 0.3) * 0.15;
    float ray = exp(-abs(uv.x - rx - rAngle * uv.y) * 25.0) * exp(-uv.y * 2.5) * 0.04;
    caustic += ray;
  }
  col += caustic * mix(vec3(0.08, 0.20, 0.40), vec3(0.15, 0.30, 0.50), u_light);

  // ── jellyfish ──────────────────────────────────────────────────
  float jfSum = 0.0;
  for (int j = 0; j < 4; j++) {
    float seed = float(j) * 83.0 + 31.0;
    // drift position
    float jx = 0.14 + float(j) * 0.24 + sin(t * 0.4 + seed) * 0.08;
    float jy = 0.22 + mod(float(j) * 0.21, 0.62) + cos(t * 0.35 + seed + 1.5) * 0.06;
    float phase = t * 0.8 + seed;

    vec2 jd = (uv - vec2(jx, jy)) * vec2(aspect * 1.05, 1.0);

    // bell: half ellipse
    float bellDist = length(jd * vec2(1.0, 2.25));
    float bell = (1.0 - smoothstep(0.095, 0.13, bellDist)) * step(jd.y, 0.025);
    float bellRim = exp(-abs(bellDist - 0.112) * 95.0) * step(jd.y, 0.035);

    // bell pulses
    float pulse = 1.0 + sin(phase) * 0.15;
    bell *= pulse;

    // tentacles: sinusoidal curves hanging down
    float tentacles = 0.0;
    for (int tn = 0; tn < 3; tn++) {
      float tx = jd.x - (float(tn) - 1.0) * 0.04;
      float wave = sin(jd.y * 12.0 + phase + float(tn) * 1.5) * 0.03;
      float tentDist = abs(tx - wave);
      float tentLen = 0.08 + float(tn) * 0.04;
      float tentWidth = 1.0 - smoothstep(0.002, 0.012, tentDist);
      float tentFade = 1.0 - smoothstep(0.0, tentLen, jd.y);
      tentacles += tentWidth * tentFade * step(0.0, jd.y);
    }

    jfSum += (bell * 0.65 + bellRim + tentacles * 0.85) * 0.82;
  }

  vec3 jfColor = mix(vec3(0.15, 0.75, 0.70), vec3(0.25, 0.80, 0.75), u_light);
  col += jfSum * jfColor * 1.05;

  // ── floating bioluminescent particles ──────────────────────────
  float particles = 0.0;
  for (int p = 0; p < 24; p++) {
    float seed = float(p) * 53.0 + 17.0;
    float px = hash1f(seed + 0.2);
    float py = fract(hash1f(seed + 0.5) + t * (0.06 + hash1f(seed + 0.8) * 0.08));
    float size = 0.004 + hash1f(seed + 0.3) * 0.010;
    float bright = 0.5 + 0.5 * sin(t * 2.5 + seed);
    float pd = length((uv - vec2(px, py)) * vec2(aspect, 1.0)) / size;
    particles += smoothstep(1.5, 0.0, pd) * bright * 0.5;
  }
  vec3 partColor = mix(vec3(0.2, 0.85, 0.7), vec3(0.35, 0.9, 0.78), u_light);
  col += particles * partColor;

  // vignette
  col *= 1.0 - length((uv - 0.5) * vec2(1.0, 0.7)) * 0.4;

  gl_FragColor = vec4(col, 1.0);
}`);

// ── silk ─────────────────────────────────────────────────────────────
// Slow premium iridescent fabric/ribbons, soft specular folds.

const FRAG_SILK = frag(`#version 100
precision highp float;
varying vec2 v_uv;
uniform vec2 u_res;
uniform float u_time;
uniform float u_light;

// HSL → RGB for iridescent colouring
vec3 hsl2rgb(float h, float s, float l) {
  vec3 rgb = clamp(
    abs(mod(h * 6.0 + vec3(0.0, 4.0, 2.0), 6.0) - 3.0) - 1.0,
    0.0, 1.0
  );
  return l + s * (rgb - 0.5) * (1.0 - abs(2.0 * l - 1.0));
}

void main() {
  vec2 uv = vec2(v_uv.x, 1.0 - v_uv.y);
  float aspect = u_res.x / u_res.y;
  float t = u_time * 0.04;

  // ── ribbon folds via layered sine waves ────────────────────────
  vec2 p = uv * vec2(aspect * 1.2, 1.0);

  float fold = sin(p.x * 4.5 + t * 0.25 + sin(p.y * 3.0 + t * 0.15) * 1.8);
  fold += sin(p.x * 7.0 - t * 0.35 + p.y * 2.0) * 0.6;
  fold += sin(p.x * 13.0 + t * 0.5) * 0.3;
  fold += fbm(p * vec2(2.0, 1.5) + t * 0.1) * 0.4;
  fold = fold * 0.45 + 0.5;

  // fold gradient for specular and colour
  float foldGrad = abs(fold - 0.5) * 2.0; // 0 at trough/crest, 1 at steep slopes

  // ── iridescent colour ──────────────────────────────────────────
  float hue = 0.59 + fold * 0.17 + t * 0.012;
  hue += sin(p.y * 1.5 + t * 0.08) * 0.025;

  float sat = 0.32 + foldGrad * 0.20;
  float lit = 0.07 + fold * 0.20;
  lit += foldGrad * 0.06;

  vec3 baseColor = hsl2rgb(hue, sat, lit);
  baseColor = mix(baseColor, baseColor * 1.3, u_light);

  // ── specular highlights ────────────────────────────────────────
  float spec = pow(max(0.0, 1.0 - abs(fold - 0.62)), 12.0) * 0.25;
  spec += pow(max(0.0, 1.0 - abs(fold - 0.28)), 16.0) * 0.15;

  vec3 col = baseColor + spec * mix(vec3(0.4, 0.45, 0.55), vec3(0.7, 0.73, 0.8), u_light);

  // subtle secondary ribbon layer
  float fold2 = sin(p.x * 3.2 - t * 0.3 + p.y * 2.5 + 2.0) * 0.5 + 0.5;
  float alpha2 = smoothstep(0.45, 0.55, fold2) * 0.15;
  float hue2 = fract(hue + 0.10);
  vec3 color2 = hsl2rgb(hue2, sat * 0.8, lit * 1.3);
  col = mix(col, color2, alpha2);

  // ── background gradient ────────────────────────────────────────
  vec3 bg = mix(vec3(0.012, 0.018, 0.055), vec3(0.045, 0.055, 0.13), u_light);
  col = bg + col * 0.92;

  // vignette
  col *= 1.0 - length((uv - 0.5) * vec2(1.0, 0.7)) * 0.35;

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

void main() {
  vec2 uv = vec2(v_uv.x, 1.0 - v_uv.y);
  float aspect = u_res.x / u_res.y;
  float t = u_time * 0.05;

  // ── sky gradient ───────────────────────────────────────────────
  float skyHorizon = 0.35;
  vec3 skyTop = mix(vec3(0.08, 0.10, 0.25), vec3(0.18, 0.22, 0.38), u_light);
  vec3 skyMid = mix(vec3(0.45, 0.25, 0.12), vec3(0.65, 0.45, 0.25), u_light);
  vec3 skyLow = mix(vec3(0.70, 0.40, 0.15), vec3(0.85, 0.58, 0.28), u_light);
  float skyGrad = smoothstep(0.0, skyHorizon, uv.y);
  vec3 sky = mix(skyTop, skyMid, smoothstep(0.0, skyHorizon * 0.7, uv.y));
  sky = mix(sky, skyLow, smoothstep(skyHorizon * 0.6, skyHorizon, uv.y));

  // sun glow near horizon
  float sunX = 0.30;
  float sunGlow = exp(-abs(uv.x - sunX) * 3.5) * exp(-abs(uv.y - skyHorizon) * 8.0);
  sky += sunGlow * mix(vec3(0.60, 0.35, 0.10), vec3(0.75, 0.50, 0.22), u_light) * 0.3;

  vec3 col = sky;

  // ── dune layers ────────────────────────────────────────────────
  for (int d = 0; d < 4; d++) {
    float df = float(d);
    float baseY = skyHorizon + 0.06 + df * 0.16;
    float amp = 0.075 + df * 0.020;

    // dune shape
    float dune = sin(uv.x * (2.0 + df * 0.8) + df * 2.7 + t * 0.02 * df) * amp;
    dune += sin(uv.x * (4.5 + df * 1.1) - df * 3.5 + t * 0.03) * amp * 0.48;
    dune += fbm(vec2(uv.x * aspect * (4.0 + df * 1.2), df * 5.0) + t * 0.04) * amp * 0.6;

    float duneY = baseY + dune;
    float duneMask = smoothstep(duneY - 0.003, duneY + 0.003, uv.y);

    // slope-based lighting (grazing light from left)
    float slope = (dune - (-amp)) / (2.0 * amp);
    float lit = 0.25 + slope * 0.75;
    lit = lit * 0.7 + 0.3;

    // dune colour: warm sand
    vec3 duneColor = mix(
      mix(vec3(0.50, 0.30, 0.14), vec3(0.68, 0.48, 0.28), u_light),
      mix(vec3(0.38, 0.22, 0.10), vec3(0.52, 0.35, 0.20), u_light),
      df * 0.25
    );
    duneColor *= lit;

    col = mix(col, duneColor, duneMask);
  }

  // ── wind-blown sand streaks ────────────────────────────────────
  float streaks = 0.0;
  for (int s = 0; s < 6; s++) {
    float sx = fract(float(s) * 0.17 + t * 0.06);
    float sy = skyHorizon + 0.12 + float(s) * 0.08;
    float streak = exp(-abs(uv.y - sy) * 30.0) * exp(-abs(uv.x - sx) * 4.0) * 0.06;
    streaks += streak;
  }
  col += streaks * mix(vec3(0.55, 0.35, 0.18), vec3(0.7, 0.52, 0.32), u_light);

  // vignette
  col *= 1.0 - length((uv - 0.5) * vec2(1.0, 0.7)) * 0.25;

  gl_FragColor = vec4(col, 1.0);
}`);

// ── raincity ─────────────────────────────────────────────────────────
// Rain on glass, defocused city bokeh and moving reflections.

const FRAG_RAINCITY = frag(`#version 100
precision highp float;
varying vec2 v_uv;
uniform vec2 u_res;
uniform float u_time;
uniform float u_light;

float hash1f(float n) {
  return fract(sin(n) * 43758.5453);
}

void main() {
  vec2 uv = vec2(v_uv.x, 1.0 - v_uv.y);
  float aspect = u_res.x / u_res.y;
  float t = u_time * 0.08;

  // ── dark night background ──────────────────────────────────────
  vec3 bg = mix(vec3(0.015, 0.018, 0.040), vec3(0.030, 0.035, 0.065), u_light);
  vec3 col = bg;

  // ── city bokeh (defocused lights) ──────────────────────────────
  float bokeh = 0.0;
  vec3 bokehColor = vec3(0.0);
  for (int b = 0; b < 16; b++) {
    float seed = float(b) * 67.0 + 23.0;
    float bx = hash1f(seed + 0.1) * 0.85 + 0.08;
    float by = hash1f(seed + 0.2) * 0.55 + 0.10;
    float size = 0.020 + hash1f(seed + 0.3) * 0.045;

    // slow drift
    bx += sin(t * 0.3 + seed) * 0.02;
    by += cos(t * 0.25 + seed + 1.0) * 0.015;

    // brightness pulses
    float bright = 0.5 + 0.5 * sin(t * 1.5 + seed);
    bright *= 0.6 + 0.4 * hash1f(seed + 0.7);

    float bd = length((uv - vec2(bx, by)) * vec2(aspect, 1.0)) / size;
    float bdot = smoothstep(2.5, 0.0, bd) * bright;

    // varied bokeh colours: warm amber, cyan, magenta
    float hue = hash1f(seed + 0.5);
    vec3 bClr;
    if (hue < 0.33) bClr = vec3(0.90, 0.50, 0.15);
    else if (hue < 0.66) bClr = vec3(0.15, 0.55, 0.80);
    else bClr = vec3(0.70, 0.20, 0.55);

    bokeh += bdot * 0.35;
    bokehColor += bdot * bClr * 0.75;
  }
  col += bokehColor * 1.15;

  // ── rain streaks ───────────────────────────────────────────────
  float rain = 0.0;
  for (int r = 0; r < 24; r++) {
    float seed = float(r) * 47.0 + 11.0;
    float rx = hash1f(seed + 0.2);
    float ry = fract(hash1f(seed + 0.4) + t * (0.8 + hash1f(seed + 0.6) * 0.5));
    float rLen = 0.04 + hash1f(seed + 0.1) * 0.08;
    float rWidth = 0.0015 + hash1f(seed + 0.3) * 0.002;

    // streak: bright at top, fades down
    float rDist = abs(uv.x - rx) / rWidth;
    float rVert = (ry - uv.y) / rLen;
    float streak = smoothstep(1.2, 0.0, rDist) * smoothstep(0.0, 1.0, rVert) * smoothstep(0.0, 0.5, rVert);
    streak *= 0.52;

    rain += streak;
  }
  col += rain * mix(vec3(0.45, 0.55, 0.70), vec3(0.60, 0.68, 0.78), u_light);

  // ── horizontal light reflections (wet ground/glass) ────────────
  float refl = 0.0;
  for (int rf = 0; rf < 5; rf++) {
    float seed = float(rf) * 37.0 + 91.0;
    float ry = hash1f(seed + 0.3) * 0.6 + 0.2;
    float rx = fract(hash1f(seed + 0.5) + t * 0.12);
    float rLen = 0.06 + hash1f(seed + 0.1) * 0.10;

    float rd = abs(uv.y - ry) * 15.0;
    float rHoriz = smoothstep(rLen, 0.0, abs(uv.x - rx));
    refl += exp(-rd) * rHoriz * 0.22;
  }
  vec3 reflColor = mix(vec3(0.55, 0.35, 0.20), vec3(0.70, 0.50, 0.35), u_light);
  col += refl * reflColor;

  // vignette
  col *= 1.0 - length((uv - 0.5) * vec2(1.0, 0.7)) * 0.40;

  gl_FragColor = vec4(col, 1.0);
}`);

// ── scene registry ──────────────────────────────────────────────────

const SCENES: Record<SceneName, SceneDef> = {
  waves: { fragmentShader: FRAG_WAVES },
  aurora: { fragmentShader: FRAG_AURORA },
  nebula: { fragmentShader: FRAG_NEBULA },
  embers: { fragmentShader: FRAG_EMBERS },
  starfield: { fragmentShader: FRAG_STARFIELD },
  blackhole: { fragmentShader: FRAG_BLACKHOLE },
  moonclouds: { fragmentShader: FRAG_MOONCLOUDS },
  biolume: { fragmentShader: FRAG_BIOLUME },
  silk: { fragmentShader: FRAG_SILK },
  dunes: { fragmentShader: FRAG_DUNES },
  raincity: { fragmentShader: FRAG_RAINCITY },
};

// ── shared engine constants ─────────────────────────────────────────

const VERTICES = new Float32Array([-1, -1, 3, -1, -1, 3]);
const MAX_DPR = 1.5;
const TARGET_FPS = 30;
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
}

export function DynamicWallpaper({ scene }: DynamicWallpaperProps) {
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
    let animTime = 0;
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
      } else {
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
      observer = new ResizeObserver(() => resize());
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
      } else if (!reducedMotion) {
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
        if (!reducedMotion && document.visibilityState !== "hidden") scheduleRAF();
      }
    }
    canvas.addEventListener("webglcontextlost", onContextLost);
    canvas.addEventListener("webglcontextrestored", onContextRestored);

    // ── startup ───────────────────────────────────────────────────
    if (init()) {
      renderOneFrame(0);
      if (!reducedMotion && document.visibilityState !== "hidden") scheduleRAF();
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
  }, [scene]);

  return (
    <canvas
      ref={canvasRef}
      className="session-background__dynamic"
      aria-hidden="true"
    />
  );
}
