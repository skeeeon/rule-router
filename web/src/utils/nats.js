// NATS WebSocket client wrapper for KV operations.
// Uses @nats-io/nats-core for connection and @nats-io/kv for KV operations.

import { wsconnect, credsAuthenticator } from '@nats-io/nats-core'
import { Kvm } from '@nats-io/kv'

function buildOpts(url, creds) {
  const opts = { servers: url }
  if (creds) {
    opts.authenticator = credsAuthenticator(new TextEncoder().encode(creds))
  }
  return opts
}

export async function testConnection({ url, creds }) {
  const nc = await wsconnect(buildOpts(url, creds))
  await nc.close()
}

export async function connectAndPush({ url, creds, bucket, key, yamlString }) {
  const nc = await wsconnect(buildOpts(url, creds))
  try {
    const kvm = new Kvm(nc)
    const kv = await kvm.open(bucket)
    await kv.put(key, new TextEncoder().encode(yamlString))
  } finally {
    await nc.close()
  }
}

export async function pushMultiple({ url, creds, bucket, files }) {
  const nc = await wsconnect(buildOpts(url, creds))
  try {
    const kvm = new Kvm(nc)
    const kv = await kvm.open(bucket)
    for (const f of files) {
      await kv.put(f.file, new TextEncoder().encode(f.yaml))
    }
  } finally {
    await nc.close()
  }
}

// Pull all keys from a KV bucket. Returns [{ key, yaml }].
export async function pullFromKV({ url, creds, bucket }) {
  const nc = await wsconnect(buildOpts(url, creds))
  try {
    const kvm = new Kvm(nc)
    const kv = await kvm.open(bucket)
    const keys = await kv.keys()
    const entries = []
    for await (const key of keys) {
      const entry = await kv.get(key)
      if (entry && entry.value) {
        entries.push({
          key,
          yaml: new TextDecoder().decode(entry.value),
        })
      }
    }
    return entries
  } finally {
    await nc.close()
  }
}
