package solutions.saubeo.rdioscanner.data.client

import android.system.Os
import android.system.OsConstants
import android.util.Log
import java.io.FileDescriptor
import java.net.InetAddress
import java.net.Socket
import javax.net.SocketFactory

/**
 * Drop-in [SocketFactory] that enables TCP-level keepalive with short idle /
 * probe intervals on every socket OkHttp opens.
 *
 * Why this exists: the WS connection drops during screen-off and other quiet
 * periods even with our app foreground-pinned and a Wi-Fi lock held. The
 * cause is that OkHttp's `pingInterval` runs on a `ScheduledExecutorService`
 * that Android can still defer under light-Doze / app-standby, so during
 * idle periods nothing crosses the socket and the reverse proxy in front of
 * the server (Cloudflare's 100s idle ceiling is the common one) closes the
 * connection as "stale". Kernel keepalive bypasses this entirely: the OS
 * TCP stack sends the probes itself, on its own clock, regardless of whether
 * our app threads are scheduled.
 *
 * The defaults here — 30s of idle before the first probe, 10s between
 * probes, 3 probes — give us a probe roughly every 10 seconds during quiet
 * periods, well under Cloudflare's 100s idle ceiling, and detect a dead
 * connection within ~60s of failure (idle + 3 * intvl).
 *
 * Reflection into `java.net.Socket.impl.fd` is required because the public
 * `java.net.Socket` API exposes `setKeepAlive(true)` (the on/off bit) but
 * not the per-socket keepalive timing knobs. On Android, `java.net.*` is
 * not subject to the hidden-API restriction (only `android.*` classes
 * are), so this reflection is allowed on every supported API level. If it
 * fails for any reason we fall back to plain `setKeepAlive(true)` which
 * uses the OS defaults (Linux: 7200s idle — useless on its own, but at
 * least it's set).
 */
class KeepAliveSocketFactory(
    private val delegate: SocketFactory = getDefault(),
    private val idleSeconds: Int = 30,
    private val intervalSeconds: Int = 10,
    private val probeCount: Int = 3,
) : SocketFactory() {

    override fun createSocket(): Socket = configure(delegate.createSocket())

    override fun createSocket(host: String?, port: Int): Socket =
        configure(delegate.createSocket(host, port))

    override fun createSocket(
        host: String?,
        port: Int,
        localHost: InetAddress?,
        localPort: Int,
    ): Socket = configure(delegate.createSocket(host, port, localHost, localPort))

    override fun createSocket(host: InetAddress?, port: Int): Socket =
        configure(delegate.createSocket(host, port))

    override fun createSocket(
        address: InetAddress?,
        port: Int,
        localAddress: InetAddress?,
        localPort: Int,
    ): Socket = configure(delegate.createSocket(address, port, localAddress, localPort))

    private fun configure(socket: Socket): Socket {
        try {
            socket.keepAlive = true
        } catch (t: Throwable) {
            Log.w(TAG, "setKeepAlive failed: ${t.message}")
            return socket
        }
        val fd = socketFileDescriptor(socket) ?: run {
            Log.d(TAG, "TCP keepalive: kept SO_KEEPALIVE=true with OS defaults (no FD access)")
            return socket
        }
        try {
            // android.system.OsConstants doesn't expose TCP_KEEPIDLE /
            // TCP_KEEPINTVL / TCP_KEEPCNT as public fields, but the
            // underlying Linux kernel constants are stable across versions
            // (uapi/linux/tcp.h).
            Os.setsockoptInt(fd, OsConstants.IPPROTO_TCP, TCP_KEEPIDLE, idleSeconds)
            Os.setsockoptInt(fd, OsConstants.IPPROTO_TCP, TCP_KEEPINTVL, intervalSeconds)
            Os.setsockoptInt(fd, OsConstants.IPPROTO_TCP, TCP_KEEPCNT, probeCount)
            Log.d(TAG, "TCP keepalive: idle=${idleSeconds}s intvl=${intervalSeconds}s probes=$probeCount")
        } catch (t: Throwable) {
            Log.w(TAG, "TCP keepalive setsockopt failed: ${t.message}")
        }
        return socket
    }

    private fun socketFileDescriptor(socket: Socket): FileDescriptor? = try {
        val implField = Socket::class.java.getDeclaredField("impl")
        implField.isAccessible = true
        val impl = implField.get(socket) ?: return null

        // Walk up the SocketImpl class hierarchy looking for an `fd` field.
        // SocksSocketImpl extends PlainSocketImpl on some platforms; field
        // is declared on the parent.
        var cls: Class<*>? = impl.javaClass
        var fdField: java.lang.reflect.Field? = null
        while (cls != null && fdField == null) {
            fdField = runCatching { cls!!.getDeclaredField("fd") }.getOrNull()
            cls = cls.superclass
        }
        fdField?.isAccessible = true
        fdField?.get(impl) as? FileDescriptor
    } catch (t: Throwable) {
        Log.w(TAG, "socket FD reflection failed: ${t.message}")
        null
    }

    companion object {
        private const val TAG = "KeepAliveSocketFactory"
        // Linux uapi/linux/tcp.h — stable across kernels.
        private const val TCP_KEEPIDLE = 4
        private const val TCP_KEEPINTVL = 5
        private const val TCP_KEEPCNT = 6
    }
}
