// Robustness checks: inputs that crash the ORIGINAL Node server (each one
// kills the process). Opt-in with NORAY_ROBUSTNESS=1, against a disposable
// server with default limits; noray-go must survive all of them.
import { describe, it, before, after } from 'node:test'
import assert from 'node:assert'
import { End2EndContext, sleep, udpSocket } from './context.mjs'

const enabled = !!process.env.NORAY_ROBUSTNESS
const context = new End2EndContext()

async function alive () {
  try {
    const tcp = await context.connect()
    const [oid] = await context.registerHost(tcp)
    return !!oid
  } catch {
    return false
  }
}

describe('Robustness', { skip: !enabled, concurrency: false }, () => {
  before(async () => { await context.startup() })

  it('survives a response to an unknown exchange ("x.1")', async () => {
    const tcp = await context.connect()
    tcp.write('connect.1 hello\n')
    await sleep(0.2)
    assert(await alive(), 'server died')
  })

  it('survives a line longer than 16 KiB', async () => {
    const tcp = await context.connect()
    tcp.write('x'.repeat(17000))
    await sleep(0.2)
    assert(await alive(), 'server died')
  })

  it('survives a relay going over the per-relay bandwidth limit', async () => {
    const parties = []
    for (let i = 0; i < 2; i++) {
      const tcp = await context.connect()
      const [oid, pid] = await context.registerHost(tcp)
      const udp = await udpSocket()
      await context.registerExternal(udp, pid)
      parties.push({ tcp, oid, udp })
    }
    const [host, client] = parties
    client.tcp.write(`connect-relay ${host.oid}\n`)
    const relay = parseInt((await context.read(client.tcp)).find(l => l.startsWith('connect-relay ')).split(' ')[1])
    // 200 x 1 KiB in one go: far over 128 KiB/s in a 100 ms slice
    const payload = Buffer.alloc(1024)
    for (let i = 0; i < 200; i++) client.udp.send(payload, relay, context.target.host)
    await sleep(0.3)
    parties.forEach(p => p.udp.close())
    assert(await alive(), 'server died')
  })

  after(() => context.shutdown())
})
