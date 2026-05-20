package solutions.saubeo.rdioscanner.data.client

import android.content.Context
import android.net.ConnectivityManager
import android.net.Network
import android.net.NetworkCapabilities
import android.net.NetworkRequest
import android.net.wifi.WifiManager
import android.os.PowerManager
import android.util.Log
import androidx.core.content.getSystemService
import kotlinx.coroutines.suspendCancellableCoroutine
import kotlinx.coroutines.withTimeoutOrNull
import java.util.concurrent.atomic.AtomicBoolean
import kotlin.coroutines.resume

/**
 * Thin wrapper around [ConnectivityManager] that lets callers wait for the
 * system to report an internet-capable network before issuing requests.
 *
 * Motivation: when the activity resumes from background, our `ON_RESUME`
 * hook fires immediately and triggers a reconnect. On many devices the
 * Wi-Fi/cell radios are still being brought back up; DNS resolution races
 * the restoration and fails with `UnknownHostException`. Without this
 * gate, the WebSocket retries enter exponential backoff (1s → 2 → 4 →
 * 8 → 16 → 30s plateau) and the user gives up and taps Connect manually.
 */
class NetworkMonitor(context: Context) {
    private val appContext = context.applicationContext
    private val cm = appContext.getSystemService<ConnectivityManager>()
    private val wifiManager = appContext.getSystemService<WifiManager>()

    // High-perf Wi-Fi lock: keeps the radio awake at full performance while
    // held. Without it, Samsung (and most OEMs) put Wi-Fi into power-save
    // mode when the screen turns off, which starves our OkHttp ping
    // schedule even though we have an FGS — the radio just isn't online to
    // send/receive packets between batched DTIM intervals. Reference-counted
    // = false so a stray double-acquire can't pin the radio after release.
    private val wifiLock: WifiManager.WifiLock? = wifiManager
        ?.createWifiLock(WifiManager.WIFI_MODE_FULL_HIGH_PERF, "rdio-scanner-ws")
        ?.apply { setReferenceCounted(false) }

    // Partial wake lock: keeps the CPU schedulable while the WS is connected
    // so OkHttp's pingInterval scheduler actually fires on time during light-
    // Doze. WiFi lock alone keeps the radio on but doesn't prevent the CPU
    // from sleeping between alarms — and when the CPU sleeps, our ping
    // tasks queue up but don't run. Battery-expensive but acceptable for a
    // live-feed app the user has explicitly chosen to keep listening; the
    // FGS notification makes it visible.
    private val powerManager = appContext.getSystemService<PowerManager>()
    private val wakeLock: PowerManager.WakeLock? = powerManager
        ?.newWakeLock(PowerManager.PARTIAL_WAKE_LOCK, "rdio-scanner-ws:cpu")
        ?.apply { setReferenceCounted(false) }

    fun acquireWifiLock() {
        val wl = wifiLock
        if (wl != null && !wl.isHeld) {
            try {
                wl.acquire()
                Log.d(TAG, "Wi-Fi high-perf lock acquired")
            } catch (t: Throwable) {
                Log.w(TAG, "acquireWifiLock failed: ${t.message}")
            }
        }
        val pl = wakeLock
        if (pl != null && !pl.isHeld) {
            try {
                pl.acquire()
                Log.d(TAG, "Partial wake lock acquired")
            } catch (t: Throwable) {
                Log.w(TAG, "acquireWakeLock failed: ${t.message}")
            }
        }
    }

    fun releaseWifiLock() {
        val wl = wifiLock
        if (wl != null && wl.isHeld) {
            try {
                wl.release()
                Log.d(TAG, "Wi-Fi high-perf lock released")
            } catch (t: Throwable) {
                Log.w(TAG, "releaseWifiLock failed: ${t.message}")
            }
        }
        val pl = wakeLock
        if (pl != null && pl.isHeld) {
            try {
                pl.release()
                Log.d(TAG, "Partial wake lock released")
            } catch (t: Throwable) {
                Log.w(TAG, "releaseWakeLock failed: ${t.message}")
            }
        }
    }

    companion object {
        private const val TAG = "NetworkMonitor"
    }

    /**
     * `true` if the system currently has an active network with the
     * `INTERNET` capability. Doesn't guarantee DNS works — Android can
     * report a network as available a beat before the resolver picks up
     * the new nameserver list — but it's a strong correlate.
     */
    fun isNetworkAvailable(): Boolean {
        val mgr = cm ?: return true
        val active = mgr.activeNetwork ?: return false
        val caps = mgr.getNetworkCapabilities(active) ?: return false
        return caps.hasCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET)
    }

    /**
     * Suspends until the system reports an available internet-capable
     * network, or [timeoutMs] elapses. Returns true on availability, false
     * on timeout (or if `ConnectivityManager` isn't reachable).
     *
     * Safe to call when network is already up — returns true immediately
     * without registering a callback.
     */
    suspend fun awaitNetwork(timeoutMs: Long = 15_000L): Boolean {
        if (isNetworkAvailable()) return true
        val mgr = cm ?: return false
        val granted = withTimeoutOrNull(timeoutMs) {
            suspendCancellableCoroutine { cont ->
                val request = NetworkRequest.Builder()
                    .addCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET)
                    .build()
                val unregistered = AtomicBoolean(false)
                lateinit var callback: ConnectivityManager.NetworkCallback

                fun cleanup() {
                    if (unregistered.compareAndSet(false, true)) {
                        try {
                            mgr.unregisterNetworkCallback(callback)
                        } catch (_: Throwable) {
                            // unregister can throw if the callback was never
                            // successfully registered or has already been
                            // removed — neither matters here.
                        }
                    }
                }

                callback = object : ConnectivityManager.NetworkCallback() {
                    override fun onAvailable(network: Network) {
                        cleanup()
                        if (cont.isActive) cont.resume(true)
                    }
                }

                try {
                    mgr.registerNetworkCallback(request, callback)
                } catch (t: Throwable) {
                    if (cont.isActive) cont.resume(false)
                    return@suspendCancellableCoroutine
                }

                cont.invokeOnCancellation { cleanup() }
            }
        }
        return granted == true
    }
}
