import { registerHooks } from "node:module";

const staticAsset = /\.(?:css|gif|jpe?g|png|svg|webp)(?:\?.*)?$/i;
const gsapStubs = new Map([
  ["gsap", "test-stub:gsap"],
  ["@gsap/react", "test-stub:gsap-react"],
  ["gsap/Flip", "test-stub:gsap-flip"],
  ["gsap/ScrollToPlugin", "test-stub:gsap-scroll"],
]);

registerHooks({
  resolve(specifier, context, nextResolve) {
    const stub = gsapStubs.get(specifier);
    if (stub) return { shortCircuit: true, url: stub };
    if (!staticAsset.test(specifier)) return nextResolve(specifier, context);
    return {
      shortCircuit: true,
      url: new URL(specifier, context.parentURL).href,
    };
  },
  load(url, context, nextLoad) {
    if (url === "test-stub:gsap") {
      return {
        format: "module",
        shortCircuit: true,
        source: `
          const tween = { kill() {}, revert() {} };
          const gsap = {
            registerPlugin() {},
            context(callback) { callback?.(); return tween; },
            fromTo(_target, _from, config) { config?.onComplete?.(); return tween; },
            killTweensOf() {},
            set() { return tween; },
            to(_target, config) { config?.onComplete?.(); return tween; },
          };
          export default gsap;
        `,
      };
    }
    if (url === "test-stub:gsap-react") {
      return { format: "module", shortCircuit: true, source: "export function useGSAP() {}" };
    }
    if (url === "test-stub:gsap-flip") {
      return { format: "module", shortCircuit: true, source: "export const Flip = {};" };
    }
    if (url === "test-stub:gsap-scroll") {
      return { format: "module", shortCircuit: true, source: "export const ScrollToPlugin = {};" };
    }
    if (!staticAsset.test(url)) return nextLoad(url, context);
    return {
      format: "module",
      shortCircuit: true,
      source: `export default ${JSON.stringify(url)};`,
    };
  },
});
