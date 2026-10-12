// Plasma regeneration: full-screen white noise, a new random grey value for every pixel in
// every frame, so each cell of a burnt-in plasma panel is driven evenly. WebGL draws it in
// a fragment shader at the native resolution; without WebGL (or high float precision, which
// the hash needs to stay pattern-free) a 2D canvas at half resolution takes over.

export type NoiseBackend = 'webgl' | '2d' | 'none';

export interface Noise {
  start(): void;
  stop(): void;
  readonly running: boolean;
  readonly backend: NoiseBackend;
}

const FRAME_MS = 1000 / 30; // 30 new frames per second are plenty and spare the Raspberry Pi

const VERTEX = `attribute vec2 p;
void main() { gl_Position = vec4(p, 0.0, 1.0); }`;

// hash(): "Hash without Sine", Copyright (c) 2014 David Hoskins, MIT License (see
// THIRD_PARTY_LICENSES.md). No visible structure with 32-bit floats, unlike sin()-based hashes.
const FRAGMENT = `precision highp float;
uniform vec2 seed;
float hash(vec2 p) {
  vec3 p3 = fract(vec3(p.xyx) * 0.1031);
  p3 += dot(p3, p3.yzx + 33.33);
  return fract((p3.x + p3.y) * p3.z);
}
void main() {
  float v = hash(gl_FragCoord.xy + seed);
  gl_FragColor = vec4(v, v, v, 1.0);
}`;

/** Fills 32-bit RGBA pixels with random grey values (xorshift32); returns the next state. */
export function fillNoise(px: Uint32Array, state: number): number {
  let s = state | 0 || 1;
  for (let i = 0; i < px.length; i++) {
    s ^= s << 13;
    s ^= s >>> 17;
    s ^= s << 5;
    const v = s & 0xff;
    px[i] = (0xff000000 | (v << 16) | (v << 8) | v) >>> 0; // R = G = B: endianness does not matter
  }
  return s;
}

interface Painter {
  readonly backend: NoiseBackend;
  frame(): void;
  dispose(): void;
}

function webglPainter(canvas: HTMLCanvasElement, onLost: () => void): Painter | null {
  const gl = canvas.getContext('webgl', {
    alpha: false,
    antialias: false,
    depth: false,
    stencil: false,
    preserveDrawingBuffer: false,
    powerPreference: 'high-performance',
  }) as WebGLRenderingContext | null;
  if (!gl) return null;
  const high = gl.getShaderPrecisionFormat(gl.FRAGMENT_SHADER, gl.HIGH_FLOAT);
  if (!high || high.precision < 23) return null;
  const shader = (type: number, src: string): WebGLShader | null => {
    const s = gl.createShader(type);
    if (!s) return null;
    gl.shaderSource(s, src);
    gl.compileShader(s);
    return gl.getShaderParameter(s, gl.COMPILE_STATUS) ? s : null;
  };
  const vs = shader(gl.VERTEX_SHADER, VERTEX);
  const fs = shader(gl.FRAGMENT_SHADER, FRAGMENT);
  const prog = gl.createProgram();
  if (!vs || !fs || !prog) return null;
  gl.attachShader(prog, vs);
  gl.attachShader(prog, fs);
  gl.linkProgram(prog);
  if (!gl.getProgramParameter(prog, gl.LINK_STATUS)) return null;
  gl.useProgram(prog);
  const buf = gl.createBuffer();
  gl.bindBuffer(gl.ARRAY_BUFFER, buf);
  gl.bufferData(gl.ARRAY_BUFFER, new Float32Array([-1, -1, 3, -1, -1, 3]), gl.STATIC_DRAW); // one big triangle
  const loc = gl.getAttribLocation(prog, 'p');
  gl.enableVertexAttribArray(loc);
  gl.vertexAttribPointer(loc, 2, gl.FLOAT, false, 0, 0);
  const seed = gl.getUniformLocation(prog, 'seed');
  const lost = (e: Event): void => {
    e.preventDefault();
    onLost();
  };
  canvas.addEventListener('webglcontextlost', lost);
  return {
    backend: 'webgl',
    frame() {
      const dpr = window.devicePixelRatio || 1;
      const w = Math.max(1, Math.round(window.innerWidth * dpr));
      const h = Math.max(1, Math.round(window.innerHeight * dpr));
      if (canvas.width !== w || canvas.height !== h) {
        canvas.width = w;
        canvas.height = h;
      }
      gl.viewport(0, 0, w, h);
      gl.uniform2f(seed, Math.random() * 4096, Math.random() * 4096);
      gl.drawArrays(gl.TRIANGLES, 0, 3);
    },
    dispose() {
      canvas.removeEventListener('webglcontextlost', lost);
      gl.getExtension('WEBGL_lose_context')?.loseContext(); // free the GPU memory at once
    },
  };
}

function canvasPainter(canvas: HTMLCanvasElement): Painter | null {
  const ctx = canvas.getContext('2d', { alpha: false }) as CanvasRenderingContext2D | null;
  if (!ctx) return null;
  let img: ImageData | null = null;
  let state = (Math.random() * 0xffffffff) | 0;
  return {
    backend: '2d',
    frame() {
      const w = Math.max(1, Math.ceil(window.innerWidth / 2));
      const h = Math.max(1, Math.ceil(window.innerHeight / 2));
      if (!img || img.width !== w || img.height !== h) {
        canvas.width = w;
        canvas.height = h;
        img = ctx.createImageData(w, h);
      }
      state = fillNoise(new Uint32Array(img.data.buffer), state);
      ctx.putImageData(img, 0, 0);
    },
    dispose() {
      img = null;
    },
  };
}

/** Full-screen noise in host (an empty element covering the screen). */
export function createNoise(host: HTMLElement): Noise {
  let painter: Painter | null = null;
  let canvas: HTMLCanvasElement | null = null;
  let raf = 0;
  let last = 0;
  let running = false;
  let preferGL = true;

  const release = (): void => {
    painter?.dispose();
    painter = null;
    canvas?.remove();
    canvas = null;
  };
  const make = (): void => {
    release();
    for (const kind of preferGL ? (['webgl', '2d'] as const) : (['2d'] as const)) {
      const c = document.createElement('canvas');
      host.appendChild(c);
      const p =
        kind === 'webgl'
          ? webglPainter(c, () => {
              // GPU reset: continue with the 2D canvas.
              preferGL = false;
              if (running) make();
            })
          : canvasPainter(c);
      if (p) {
        canvas = c;
        painter = p;
        return;
      }
      c.remove();
    }
  };
  const tick = (now: number): void => {
    if (!running) return;
    raf = requestAnimationFrame(tick);
    if (now - last < FRAME_MS) return;
    last = now;
    painter?.frame();
  };

  return {
    start() {
      if (running) return;
      running = true;
      host.classList.remove('hidden');
      make();
      painter?.frame(); // at once, not only with the next animation frame
      last = performance.now();
      raf = requestAnimationFrame(tick);
    },
    stop() {
      if (!running) return;
      running = false;
      cancelAnimationFrame(raf);
      release();
      host.classList.add('hidden');
    },
    get running() {
      return running;
    },
    get backend() {
      return painter?.backend ?? 'none';
    },
  };
}
