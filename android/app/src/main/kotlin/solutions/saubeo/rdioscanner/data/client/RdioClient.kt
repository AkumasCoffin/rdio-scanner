package solutions.saubeo.rdioscanner.data.client

import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.SharedFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asSharedFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.launch
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.Response
import okhttp3.WebSocket
import okhttp3.WebSocketListener
import okio.ByteString
import android.util.Log
import solutions.saubeo.rdioscanner.data.protocol.CallDto
import solutions.saubeo.rdioscanner.data.protocol.ConfigDto
import solutions.saubeo.rdioscanner.data.protocol.Incoming
import solutions.saubeo.rdioscanner.data.protocol.LivefeedMap
import solutions.saubeo.rdioscanner.data.protocol.SearchOptions
import solutions.saubeo.rdioscanner.data.protocol.SearchResults
import solutions.saubeo.rdioscanner.data.protocol.ServerVersion
import solutions.saubeo.rdioscanner.data.protocol.Wire
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicBoolean
import java.util.concurrent.atomic.AtomicInteger

sealed interface ConnectionState {
    data object Disconnected : ConnectionState
    data object Connecting : ConnectionState
    data object Authenticating : ConnectionState
    data object Connected : ConnectionState
    data object AuthFailed : ConnectionState
    data object Expired : ConnectionState
    data object TooMany : ConnectionState
    data class Error(val message: String) : ConnectionState
}

data class RdioCredentials(val baseUrl: String, val accessCode: String? = null)

/**
 * WebSocket client that speaks rdio-scanner's array-framed JSON protocol.
 *
 * Upgrade endpoint is the root path. HTTP(S) URLs are rewritten to ws/wss.
 */
class RdioClient(
    private val httpClient: OkHttpClient = defaultHttpClient(),
) {
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.Default)

    private val _state = MutableStateFlow<ConnectionState>(ConnectionState.Disconnected)
    val state: StateFlow<ConnectionState> = _state.asStateFlow()

    private val _version = MutableStateFlow<ServerVersion?>(null)
    val version: StateFlow<ServerVersion?> = _version.asStateFlow()

    private val _config = MutableStateFlow<ConfigDto?>(null)
    val config: StateFlow<ConfigDto?> = _config.asStateFlow()

    private val _listeners = MutableStateFlow(0)
    val listeners: StateFlow<Int> = _listeners.asStateFlow()

    private val _livefeedActive = MutableStateFlow(false)
    val livefeedActive: StateFlow<Boolean> = _livefeedActive.asStateFlow()

    private val _calls = MutableSharedFlow<Pair<CallDto, String?>>(
        replay = 0, extraBufferCapacity = 32,
    )
    val calls: SharedFlow<Pair<CallDto, String?>> = _calls.asSharedFlow()

    private val _searchResults = MutableStateFlow<SearchResults?>(null)
    val searchResults: StateFlow<SearchResults?> = _searchResults.asStateFlow()

    private val _searching = MutableStateFlow(false)
    val searching: StateFlow<Boolean> = _searching.asStateFlow()

    private var credentials: RdioCredentials? = null
    private var webSocket: WebSocket? = null
    private var reconnectJob: Job? = null
    private var reconnectAttempts = 0
    private val shouldReconnect = AtomicBoolean(false)
    private var pendingLivefeed: LivefeedMap? = null
    /**
     * Bumped for every open attempt. Each Listener captures the generation
     * it was created at; stale listeners (still alive on an orphaned socket
     * that hasn't finished closing) compare and early-return, so a ghost
     * connection can't deliver the same CAL frame alongside the live one.
     */
    private val generation = AtomicInteger(0)

    fun connect(creds: RdioCredentials) {
        Log.d(TAG, "connect: url=${creds.baseUrl}, hadAccessCode=${creds.accessCode != null}")
        credentials = creds
        shouldReconnect.set(true)
        reconnectAttempts = 0
        // Defensively close any prior socket + pending reconnect: prevents two
        // parallel WS connections from both feeding the same SharedFlow, which
        // would make every incoming CAL play twice. Bump generation BEFORE
        // closing so the old Listener's onClosed sees gen != current() and
        // bails — otherwise it could race a Disconnected emit AFTER we set
        // state below.
        reconnectJob?.cancel()
        reconnectJob = null
        generation.incrementAndGet()
        webSocket?.close(1000, "reconnect with new credentials")
        webSocket = null
        // Force a real Connected → Disconnected → Connecting → Connected
        // transition on profile switch. Without this, profile-A's state
        // never leaves Connected (close fires onClosed but it short-circuits
        // on stale generation; openSocket only sets Connecting → Connected),
        // so the LaunchedEffect(state) in Navigation.kt never sees a
        // transition to react to and the back stack ends up in a bad shape
        // — observable as "tapping connection 2 kicks me out of the app"
        // on multi-profile setups (#1).
        _state.value = ConnectionState.Disconnected
        _config.value = null
        _version.value = null
        _livefeedActive.value = false
        Log.d(TAG, "connect: state -> Disconnected, calling openSocket()")
        openSocket()
    }

    fun disconnect() {
        shouldReconnect.set(false)
        reconnectJob?.cancel()
        reconnectJob = null
        // Invalidate the current listener so any frames still in flight on the
        // socket that's about to close are ignored and can't re-emit.
        generation.incrementAndGet()
        webSocket?.close(1000, "client disconnect")
        webSocket = null
        _state.value = ConnectionState.Disconnected
        _config.value = null
        _version.value = null
        _livefeedActive.value = false
    }

    fun setLivefeedMap(map: LivefeedMap) {
        pendingLivefeed = map
        send(Wire.livefeedMap(map))
    }

    /** Matches the webapp stopLivefeed: sends `[LFM]` with no payload; server clears its matrix. */
    fun clearLivefeed() {
        pendingLivefeed = null
        send(Wire.clearLivefeed())
    }

    fun requestCall(id: Long, flag: String? = null) {
        send(Wire.call(id, flag))
    }

    fun search(opts: SearchOptions) {
        _searching.value = true
        if (!send(Wire.listCall(opts))) {
            // Couldn't send — clear the flag immediately so the UI doesn't hang.
            _searching.value = false
        }
    }

    fun requestConfig() {
        send(Wire.config())
    }

    fun shutdown() {
        disconnect()
        scope.cancel()
    }

    private fun send(frame: String): Boolean {
        val ws = webSocket ?: return false
        return ws.send(frame)
    }

    private fun openSocket() {
        val creds = credentials ?: run {
            Log.w(TAG, "openSocket: no credentials, bailing")
            return
        }
        val url = toWsUrl(creds.baseUrl) ?: run {
            Log.w(TAG, "openSocket: invalid url '${creds.baseUrl}', state -> Error")
            _state.value = ConnectionState.Error("Invalid server URL")
            return
        }
        // Guard against any lingering socket that wasn't torn down — force
        // close before opening so there's only ever one live WS at a time.
        webSocket?.close(1000, "reopen")
        webSocket = null
        val gen = generation.incrementAndGet()
        _state.value = ConnectionState.Connecting
        Log.d(TAG, "openSocket: gen=$gen, url=$url, state -> Connecting")
        val req = Request.Builder().url(url).build()
        webSocket = httpClient.newWebSocket(req, Listener(gen))
    }

    private fun scheduleReconnect() {
        if (!shouldReconnect.get()) return
        reconnectJob?.cancel()
        val attempt = reconnectAttempts.coerceAtMost(6)
        val delayMs = (1000L shl attempt).coerceAtMost(30_000L)
        reconnectAttempts++
        reconnectJob = scope.launch {
            delay(delayMs)
            openSocket()
        }
    }

    private inner class Listener(private val gen: Int) : WebSocketListener() {
        private fun current(): Boolean = gen == generation.get()

        override fun onOpen(webSocket: WebSocket, response: Response) {
            // Already had this guard but documenting: without it a ghost socket
            // that finally completes its TCP handshake could still rewind
            // state to Authenticating and re-send VER/CFG, racing the live
            // socket's own handshake.
            if (!current()) {
                Log.d(TAG, "Listener[$gen].onOpen: stale (current=${generation.get()}), closing")
                webSocket.close(1000, "stale")
                return
            }
            Log.d(TAG, "Listener[$gen].onOpen: state -> Authenticating, sending VER + CFG")
            reconnectAttempts = 0
            _state.value = ConnectionState.Authenticating
            // Ask for version and config (server will prompt PIN if needed).
            webSocket.send(Wire.version())
            webSocket.send(Wire.config())
        }

        override fun onMessage(webSocket: WebSocket, text: String) {
            if (!current()) return
            val msg = Wire.decode(text) ?: return
            handle(msg)
        }

        override fun onMessage(webSocket: WebSocket, bytes: ByteString) {
            // Server only sends text frames currently; ignore.
        }

        override fun onClosing(webSocket: WebSocket, code: Int, reason: String) {
            webSocket.close(code, reason)
        }

        override fun onClosed(webSocket: WebSocket, code: Int, reason: String) {
            if (!current()) {
                Log.d(TAG, "Listener[$gen].onClosed: stale code=$code reason='$reason' (current=${generation.get()})")
                return
            }
            this@RdioClient.webSocket = null
            if (code != 1000 && shouldReconnect.get()) {
                Log.d(TAG, "Listener[$gen].onClosed: code=$code reason='$reason' -> Connecting + reconnect")
                _state.value = ConnectionState.Connecting
                scheduleReconnect()
            } else {
                Log.d(TAG, "Listener[$gen].onClosed: code=$code reason='$reason' -> Disconnected")
                _state.value = ConnectionState.Disconnected
            }
        }

        override fun onFailure(webSocket: WebSocket, t: Throwable, response: Response?) {
            if (!current()) {
                Log.d(TAG, "Listener[$gen].onFailure: stale (current=${generation.get()}): ${t.message}")
                return
            }
            this@RdioClient.webSocket = null
            if (shouldReconnect.get()) {
                Log.w(TAG, "Listener[$gen].onFailure: ${t.message} -> Error + reconnect", t)
                _state.value = ConnectionState.Error(t.message ?: "connection failed")
                scheduleReconnect()
            } else {
                Log.w(TAG, "Listener[$gen].onFailure: ${t.message} -> Disconnected (no reconnect)", t)
                _state.value = ConnectionState.Disconnected
            }
        }
    }

    private fun handle(msg: Incoming) {
        when (msg) {
            is Incoming.Version -> _version.value = msg.payload

            is Incoming.Config -> {
                Log.d(TAG, "handle: CFG received (${msg.payload.systems.size} systems), state -> Connected")
                _config.value = msg.payload
                _state.value = ConnectionState.Connected
                pendingLivefeed?.let { send(Wire.livefeedMap(it)) }
            }

            is Incoming.Call -> {
                scope.launch { _calls.emit(msg.payload to msg.flag) }
            }

            is Incoming.Listeners -> _listeners.value = msg.count

            is Incoming.LivefeedAck -> _livefeedActive.value = msg.active

            is Incoming.Search -> {
                _searchResults.value = msg.payload
                _searching.value = false
            }

            Incoming.PinRequested -> {
                val code = credentials?.accessCode
                if (!code.isNullOrEmpty()) {
                    send(Wire.pin(code))
                } else {
                    _state.value = ConnectionState.AuthFailed
                }
            }

            Incoming.Expired -> _state.value = ConnectionState.Expired
            Incoming.TooMany -> _state.value = ConnectionState.TooMany
            is Incoming.Unknown -> Unit
        }
    }

    companion object {
        private const val TAG = "RdioClient"

        fun toWsUrl(baseUrl: String): String? {
            val trimmed = baseUrl.trim().trimEnd('/')
            if (trimmed.isEmpty()) return null
            return when {
                trimmed.startsWith("wss://") || trimmed.startsWith("ws://") -> trimmed
                trimmed.startsWith("https://") -> "wss://" + trimmed.removePrefix("https://")
                trimmed.startsWith("http://") -> "ws://" + trimmed.removePrefix("http://")
                else -> "ws://$trimmed"
            }
        }

        fun defaultHttpClient(): OkHttpClient = OkHttpClient.Builder()
            .connectTimeout(15, TimeUnit.SECONDS)
            .readTimeout(0, TimeUnit.SECONDS)
            .pingInterval(30, TimeUnit.SECONDS)
            .retryOnConnectionFailure(true)
            .build()
    }
}
