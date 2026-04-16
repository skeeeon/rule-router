// WASM loader for the Go rule evaluation engine.
// Lazy-loads tester.wasm on first use and exposes evaluateRule().

let wasmReady = false
let wasmLoading = false
let wasmError = null
let loadPromise = null

export function getWasmState() {
  return { ready: wasmReady, loading: wasmLoading, error: wasmError }
}

export async function loadWasm() {
  if (wasmReady) return
  if (loadPromise) return loadPromise

  wasmLoading = true
  wasmError = null

  loadPromise = (async () => {
    try {
      const go = new globalThis.Go()
      const result = await WebAssembly.instantiateStreaming(
        fetch('/tester.wasm'),
        go.importObject,
      )
      // Run the Go main() which registers evaluateRule on globalThis
      go.run(result.instance)
      wasmReady = true
    } catch (err) {
      wasmError = err.message || 'Failed to load WASM'
      throw err
    } finally {
      wasmLoading = false
    }
  })()

  return loadPromise
}

export async function callEvaluateRule(yamlStr, messageStr, options) {
  await loadWasm()

  if (typeof globalThis.evaluateRule !== 'function') {
    throw new Error('evaluateRule not registered — WASM may have failed to initialize')
  }

  const resultJSON = globalThis.evaluateRule(yamlStr, messageStr, JSON.stringify(options))
  return JSON.parse(resultJSON)
}
