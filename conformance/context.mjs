// Black-box port of noray's test/e2e/context.mjs: instead of starting Noray
// in-process it connects to an already running server, so the same suite can
// validate the original Node server and any replacement.
//
// Target: NORAY_HOST (127.0.0.1), NORAY_SOCKET_PORT (8890),
// NORAY_UDP_REGISTRAR_PORT (8809), NORAY_HTTP_PORT (8891).
import * as net from 'node:net'
import * as dgram from 'node:dgram'
import * as http from 'node:http'
import { setTimeout as delay } from 'node:timers/promises'

const READ_WAIT = 0.05

export const target = {
  host: process.env.NORAY_HOST ?? '127.0.0.1',
  port: parseInt(process.env.NORAY_SOCKET_PORT ?? '8890'),
  registrarPort: parseInt(process.env.NORAY_UDP_REGISTRAR_PORT ?? '8809'),
  httpPort: parseInt(process.env.NORAY_HTTP_PORT ?? '8891')
}

export function sleep (seconds, value) {
  return delay(seconds * 1000, value)
}

export function promiseEvent (source, event) {
  return new Promise(resolve => source.once(event, resolve))
}

const debug = !!process.env.CONFORMANCE_DEBUG
const log = {
  info: (...args) => debug && console.log('[test]', ...args),
  debug: (...args) => debug && console.log('[test:debug]', ...args)
}

export class End2EndContext {
  #clients = []
  log = log
  target = target

  async startup () {
    this.log.info('Using noray at %s:%d', target.host, target.port)
    // Fail fast if nothing is listening
    const probe = await this.connect()
    probe.destroy()
  }

  async connect () {
    const socket = net.createConnection({ host: target.host, port: target.port })
    socket.setEncoding('utf8')

    await new Promise((resolve, reject) => {
      socket.once('connect', resolve)
      socket.once('error', reject)
    })
    this.#clients.push(socket)
    return socket
  }

  async read (socket, wait = READ_WAIT) {
    await sleep(wait)

    const lines = []
    for (let line = ''; line != null; line = socket.read()) {
      lines.push(line)
    }

    const result = lines.join('').split('\n')
    this.log.debug('Read data from noray', result)
    return result
  }

  /**
  * @param {dgram.Socket} udp
  * @param {string} pid
  */
  async registerExternal (udp, pid) {
    let done = false
    let error
    const throwaway = udp === undefined

    if (udp === undefined) {
      udp = dgram.createSocket('udp4')
      udp.bind()
      await promiseEvent(udp, 'listening')
    }

    udp.once('message', (buf, _rinfo) => {
      const msg = buf.toString('utf8')
      done = true
      error = msg !== 'OK' && msg
    })

    for (let i = 0; i < 128 && !done; ++i) {
      udp.send(pid, target.registrarPort, target.host)
      this.log.debug('Sending remote registrar attempt #%d', i + 1)
      await sleep(0.1)
    }

    if (!done) {
      throw new Error('Registrar timed out!')
    } else if (error) {
      throw new Error(error)
    }

    const result = udp.address().port
    if (throwaway) {
      udp.close()
    }

    return result
  }

  async registerHost (socket) {
    socket.write('register-host\n')

    const data = await this.read(socket)

    const oid = data
      .filter(cmd => cmd.startsWith('set-oid '))
      .map(cmd => cmd.split(' ')[1])
      .at(0)

    const pid = data
      .filter(cmd => cmd.startsWith('set-pid '))
      .map(cmd => cmd.split(' ')[1])
      .at(0)

    return [oid, pid]
  }

  shutdown () {
    this.log.info('Closing %d connections', this.#clients.length)
    this.#clients.forEach(c => c.destroy())
  }
}

/** Bind a UDP socket on an OS-picked port. */
export async function udpSocket () {
  const udp = dgram.createSocket('udp4')
  udp.bind()
  await promiseEvent(udp, 'listening')
  return udp
}

/** Fetch a path from the metrics HTTP server. */
export async function httpGet (path) {
  return new Promise((resolve, reject) => {
    http.get({ host: target.host, port: target.httpPort, path, agent: false }, res => {
      let body = ''
      res.setEncoding('utf8')
      res.on('data', d => { body += d })
      res.on('end', () => resolve({ status: res.statusCode, headers: res.headers, body }))
    }).on('error', reject)
  })
}
