// Extra drop-in compatibility checks (not in the original suite). They pin
// down observable behaviour of the original server that clients may rely on:
// exact error replies, registrar replies, relay port reuse, dynamic relaying,
// framing and the metrics endpoint. Every test must pass against the original
// Node server too.
import { describe, it, before, after } from 'node:test'
import assert from 'node:assert'
import { End2EndContext, httpGet, sleep, udpSocket } from './context.mjs'

const context = new End2EndContext()

async function party () {
  const tcp = await context.connect()
  const [oid, pid] = await context.registerHost(tcp)
  const udp = await udpSocket()
  await context.registerExternal(udp, pid)
  return { tcp, udp, oid, pid }
}

function udpRequest (udp, message, port) {
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error('UDP timeout')), 2000)
    udp.once('message', msg => { clearTimeout(timer); resolve(msg.toString()) })
    udp.send(message, port, context.target.host)
  })
}

function nextMessage (udp, timeout = 1000) {
  return new Promise(resolve => {
    const timer = setTimeout(() => resolve(undefined), timeout)
    udp.once('message', (msg, rinfo) => { clearTimeout(timer); resolve({ msg: msg.toString(), rinfo }) })
  })
}

const sockets = []

describe('Compatibility', { concurrency: false }, () => {
  before(async () => { await context.startup() })

  describe('register-host', () => {
    it('should reply with set-oid and set-pid lines', async () => {
      const tcp = await context.connect()
      tcp.write('register-host\n')
      const lines = (await context.read(tcp)).filter(l => l)
      assert.equal(lines.length, 2)
      assert.match(lines[0], /^set-oid [A-Za-z0-9_-]{21}$/)
      assert.match(lines[1], /^set-pid [A-Za-z0-9_-]{128}$/)
    })

    it('should parse commands split across writes and batched in one write', async () => {
      const tcp = await context.connect()
      tcp.write('regis')
      await sleep(0.05)
      tcp.write('ter-host\nregister-host\n')
      const lines = (await context.read(tcp)).filter(l => l)
      assert.equal(lines.filter(l => l.startsWith('set-oid ')).length, 2)
      assert.equal(lines.filter(l => l.startsWith('set-pid ')).length, 2)
    })

    it('should accept the request form', async () => {
      const tcp = await context.connect()
      tcp.write('register-host?abc\n')
      const lines = (await context.read(tcp)).filter(l => l)
      assert(lines.find(l => l.startsWith('set-oid ')))
      assert(lines.find(l => l.startsWith('set-pid ')))
    })

    it('should ignore unknown commands', async () => {
      const tcp = await context.connect()
      tcp.write('frobnicate now\n\n')
      assert.deepEqual((await context.read(tcp)).filter(l => l), [])
    })
  })

  describe('registrar', () => {
    it('should reply OK, and OK again on repeats', async () => {
      const tcp = await context.connect()
      const [, pid] = await context.registerHost(tcp)
      const udp = await udpSocket()
      sockets.push(udp)
      assert.equal(await udpRequest(udp, pid, context.target.registrarPort), 'OK')
      assert.equal(await udpRequest(udp, pid, context.target.registrarPort), 'OK')
    })

    it('should reply with the error for unknown pids', async () => {
      const udp = await udpSocket()
      sockets.push(udp)
      assert.equal(await udpRequest(udp, 'not-a-pid', context.target.registrarPort), 'Unknown host pid!')
    })

    it('should keep the first registered address', async () => {
      const host = await party()
      const client = await party()
      sockets.push(host.udp, client.udp)
      const other = await udpSocket()
      sockets.push(other)
      assert.equal(await udpRequest(other, host.pid, context.target.registrarPort), 'OK')

      client.tcp.write(`connect ${host.oid}\n`)
      const reply = (await context.read(client.tcp)).find(l => l.startsWith('connect '))
      assert.equal(parseInt(reply.split(':').at(-1)), host.udp.address().port)
    })
  })

  describe('connect', () => {
    it('should send each party the other one\'s address', async () => {
      const host = await party()
      const client = await party()
      sockets.push(host.udp, client.udp)
      client.tcp.write(`connect ${host.oid}\n`)
      const toClient = (await context.read(client.tcp)).filter(l => l)
      const toHost = (await context.read(host.tcp)).filter(l => l)
      assert.equal(toClient.length, 1)
      assert.equal(toHost.length, 1)
      assert.match(toClient[0], /^connect \d+\.\d+\.\d+\.\d+:\d+$/)
      assert.equal(parseInt(toClient[0].split(':')[1]), host.udp.address().port)
      assert.equal(parseInt(toHost[0].split(':')[1]), client.udp.address().port)
    })

    it('should reply with a bare "connect" line on unknown oid', async () => {
      const client = await party()
      sockets.push(client.udp)
      client.tcp.write('connect nosuchhost\n')
      assert.deepEqual((await context.read(client.tcp)).filter(l => l), ['connect'])
    })

    it('should reply with "!<id>" on errors in request form', async () => {
      const client = await party()
      sockets.push(client.udp)
      client.tcp.write('connect?r1 nosuchhost\n')
      assert.deepEqual((await context.read(client.tcp)).filter(l => l), ['!r1'])
    })

    it('should fail if the host has no registered address', async () => {
      const hostTcp = await context.connect()
      const [oid] = await context.registerHost(hostTcp)
      const client = await party()
      sockets.push(client.udp)
      client.tcp.write(`connect ${oid}\n`)
      assert.deepEqual((await context.read(client.tcp)).filter(l => l), ['connect'])
    })

    it('should fail if the client is not registered', async () => {
      const host = await party()
      sockets.push(host.udp)
      const tcp = await context.connect()
      tcp.write(`connect ${host.oid}\n`)
      assert.deepEqual((await context.read(tcp)).filter(l => l), ['connect'])
      assert.deepEqual((await context.read(host.tcp)).filter(l => l), [])
    })

    it('should forget hosts that disconnect', async () => {
      const host = await party()
      sockets.push(host.udp)
      const client = await party()
      sockets.push(client.udp)
      host.tcp.destroy()
      await sleep(0.1)
      client.tcp.write(`connect ${host.oid}\n`)
      assert.deepEqual((await context.read(client.tcp)).filter(l => l), ['connect'])
    })
  })

  describe('connect-relay', () => {
    it('should reply with numeric relay ports, the same ones on repeat', async () => {
      const host = await party()
      const client = await party()
      sockets.push(host.udp, client.udp)
      client.tcp.write(`connect-relay ${host.oid}\n`)
      const a = (await context.read(client.tcp)).filter(l => l)
      const b = (await context.read(host.tcp)).filter(l => l)
      assert.equal(a.length, 1)
      assert.equal(b.length, 1)
      assert.match(a[0], /^connect-relay \d+$/)
      assert.match(b[0], /^connect-relay \d+$/)
      assert.notEqual(a[0], b[0])

      client.tcp.write(`connect-relay ${host.oid}\n`)
      assert.deepEqual((await context.read(client.tcp)).filter(l => l), a)
      assert.deepEqual((await context.read(host.tcp)).filter(l => l), b)
    })

    it('should reply with a bare "connect-relay" line on unknown oid', async () => {
      const client = await party()
      sockets.push(client.udp)
      client.tcp.write('connect-relay nosuchhost\n')
      assert.deepEqual((await context.read(client.tcp)).filter(l => l), ['connect-relay'])
    })

    it('should relay both ways and dynamically relay unknown senders', async () => {
      const host = await party()
      const client = await party()
      sockets.push(host.udp, client.udp)
      client.tcp.write(`connect-relay ${host.oid}\n`)
      const hostRelay = parseInt((await context.read(client.tcp)).find(l => l.startsWith('connect-relay ')).split(' ')[1])
      const clientRelay = parseInt((await context.read(host.tcp)).find(l => l.startsWith('connect-relay ')).split(' ')[1])

      // client -> host, arrives from the client's relay port
      let got = nextMessage(host.udp)
      client.udp.send('ping', hostRelay, context.target.host)
      let m = await got
      assert.equal(m?.msg, 'ping')
      assert.equal(m.rinfo.port, clientRelay)

      // host -> client, arrives from the host's relay port
      got = nextMessage(client.udp)
      host.udp.send('pong', clientRelay, context.target.host)
      m = await got
      assert.equal(m?.msg, 'pong')
      assert.equal(m.rinfo.port, hostRelay)

      // an unregistered socket sending to the host's relay gets a relay too
      const stranger = await udpSocket()
      sockets.push(stranger)
      got = nextMessage(host.udp)
      stranger.send('hello', hostRelay, context.target.host)
      m = await got
      assert.equal(m?.msg, 'hello')
      assert.notEqual(m.rinfo.port, clientRelay)
      assert.notEqual(m.rinfo.port, hostRelay)
      const strangerRelay = m.rinfo.port

      got = nextMessage(stranger)
      host.udp.send('welcome', strangerRelay, context.target.host)
      m = await got
      assert.equal(m?.msg, 'welcome')
      assert.equal(m.rinfo.port, hostRelay)
    })

    it('should drop traffic to relay ports from unknown senders without a target', async () => {
      const stranger = await udpSocket()
      sockets.push(stranger)
      const got = nextMessage(stranger, 300)
      stranger.send('anyone?', 1, context.target.host)
      assert.equal(await got, undefined)
    })
  })

  describe('metrics', () => {
    it('should serve Prometheus metrics with the original names', async () => {
      const res = await httpGet('/metrics')
      assert.equal(res.status, 200)
      for (const name of [
        'noray_active_hosts', 'noray_relay_count', 'noray_relay_drop_count',
        'noray_relay_expired', 'noray_relay_duration_bucket', 'noray_relay_size_bucket',
        'noray_remote_registrar_success', 'noray_remote_registrar_fail',
        'noray_remote_registrar_repeat', 'process_cpu_seconds_total'
      ]) {
        assert.match(res.body, new RegExp(`^${name}`, 'm'), `missing ${name}`)
      }
      assert.match(res.body, /^# TYPE noray_relay_duration histogram$/m)
      assert.match(res.body, /^# TYPE noray_relay_drop_count counter$/m)
      assert.match(res.body, /^# TYPE noray_active_hosts gauge$/m)
    })

    it('should count active hosts', async () => {
      const value = async () => parseFloat((await httpGet('/metrics')).body.match(/^noray_active_hosts (\S+)$/m)[1])
      const before = await value()
      const tcp = await context.connect()
      await context.registerHost(tcp)
      assert.equal(await value(), before + 1)
      tcp.destroy()
      await sleep(0.1)
      assert.equal(await value(), before)
    })

    it('should 404 anything but /metrics', async () => {
      assert.equal((await httpGet('/')).status, 404)
      assert.equal((await httpGet('/metrics?x=1')).status, 404)
      assert.equal((await httpGet('/metrics/')).status, 404)
    })
  })

  after(() => {
    sockets.forEach(s => { try { s.close() } catch {} })
    context.shutdown()
  })
})
