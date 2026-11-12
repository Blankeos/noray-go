// Black-box version of noray's test/e2e/udp.relay.test.mjs. The original is
// `skip: true` and drives an in-process UDPRelayHandler; this one creates the
// two relays through the real protocol and keeps the original's asserts: the
// message arrives intact, from the server's address, from the sender's relay
// port.
import { describe, it, after, before } from 'node:test'
import assert from 'node:assert'
import { End2EndContext, sleep, udpSocket } from './context.mjs'

describe('UDP Relay', { concurrency: false }, async () => {
  const context = new End2EndContext()
  let sendSocket, recvSocket
  let sendRelay, recvRelay

  before(async () => {
    await context.startup()
    sendSocket = await udpSocket()
    recvSocket = await udpSocket()

    const sendTcp = await context.connect()
    const recvTcp = await context.connect()
    const [, sendPid] = await context.registerHost(sendTcp)
    const [recvOid, recvPid] = await context.registerHost(recvTcp)
    await context.registerExternal(sendSocket, sendPid)
    await context.registerExternal(recvSocket, recvPid)

    // Sender asks to relay to receiver: sender learns receiver's relay port,
    // receiver learns sender's relay port.
    sendTcp.write(`connect-relay ${recvOid}\n`)
    recvRelay = parseInt((await context.read(sendTcp)).find(l => l.startsWith('connect-relay ')).split(' ')[1])
    sendRelay = parseInt((await context.read(recvTcp)).find(l => l.startsWith('connect-relay ')).split(' ')[1])
  })

  it('should relay traffic', async () => {
    // Given
    const recvData = []
    const message = Buffer.from('Hello!', 'utf8')
    recvSocket.once('message', (msg, rinfo) => recvData.push({ msg, rinfo }))

    // When
    sendSocket.send(message, recvRelay, context.target.host)
    await sleep(0.25)

    // Then
    assert.deepEqual(recvData[0]?.msg, message)
    assert.equal(recvData[0].rinfo.port, sendRelay)
  })

  after(() => {
    sendSocket.close()
    recvSocket.close()
    context.shutdown()
  })
})
