package solutions.saubeo.rdioscanner.audio

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Intent
import android.content.pm.ServiceInfo
import android.os.Build
import android.util.Log
import androidx.core.app.NotificationCompat
import androidx.core.content.getSystemService
import androidx.media3.session.MediaSession
import androidx.media3.session.MediaSessionService
import solutions.saubeo.rdioscanner.MainActivity
import solutions.saubeo.rdioscanner.R
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.flow.combine
import kotlinx.coroutines.flow.distinctUntilChanged
import kotlinx.coroutines.flow.launchIn
import kotlinx.coroutines.flow.onEach
import solutions.saubeo.rdioscanner.RdioApplication
import solutions.saubeo.rdioscanner.data.client.ConnectionState
import solutions.saubeo.rdioscanner.data.protocol.CallDto
import solutions.saubeo.rdioscanner.data.protocol.ConfigDto
import solutions.saubeo.rdioscanner.data.protocol.OscillatorBeep
import solutions.saubeo.rdioscanner.data.protocol.SystemDto
import solutions.saubeo.rdioscanner.data.protocol.TalkgroupDto
import solutions.saubeo.rdioscanner.data.repository.RdioRepository.Companion.FLAG_PLAY

/**
 * MediaSessionService that owns the [CallPlayer] and bridges server calls
 * from the repository into the playback queue.
 *
 * Foreground lifecycle: this service goes foreground as soon as the WS
 * connects (with a quiet "Listening to live feed" notification) and stays
 * foreground until the user explicitly disconnects. That keeps the process
 * out of Doze, so the OkHttp ping fires on its 30s schedule and the WS
 * survives long background periods. Without this gating, MediaSessionService
 * only goes foreground while ExoPlayer is actively playing — and the silent
 * gaps between calls let Doze freeze the ping, which lets any reverse proxy
 * in front of the server (Cloudflare, nginx) close the idle socket.
 *
 * During actual playback Media3 swaps in its own media-notification with
 * playback controls; when playback ends and the player goes idle, we
 * re-assert our Listening notification so foreground state never drops
 * while we're still connected.
 */
class AudioService : MediaSessionService() {
    private lateinit var callPlayer: CallPlayer
    private var session: MediaSession? = null
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.Main.immediate)
    private var pipeJob: Job? = null
    private var foregroundJob: Job? = null
    private var foregroundActive = false

    override fun onCreate() {
        super.onCreate()
        val app = application as RdioApplication
        callPlayer = app.audio
        val launch = Intent(this, MainActivity::class.java).apply {
            addFlags(Intent.FLAG_ACTIVITY_SINGLE_TOP or Intent.FLAG_ACTIVITY_CLEAR_TOP)
        }
        val launchPi = PendingIntent.getActivity(
            this, 0, launch,
            PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
        )
        session = MediaSession.Builder(this, callPlayer.player)
            .setSessionActivity(launchPi)
            .build()

        ensureListeningChannel()

        val repo = app.repository

        // Drive our foreground state off the WS connection: as long as the
        // user has a live link to the server we show the Listening
        // notification, even between calls. Media3 takes over with its own
        // media-controls notification while a call is actually playing,
        // then we put ours back when playback ends — re-asserting via
        // startForeground() every time prevents Doze from kicking in during
        // the silent gaps.
        foregroundJob = combine(
            repo.state,
            callPlayer.isPlaying,
        ) { state, playing -> state to playing }
            .distinctUntilChanged()
            .onEach { (state, playing) ->
                val connected = state is ConnectionState.Connected ||
                    state is ConnectionState.Authenticating ||
                    state is ConnectionState.Connecting
                when {
                    connected && !playing -> startListeningForeground(state)
                    !connected && !playing -> stopListeningForeground()
                    // playing == true: Media3 owns the foreground notification
                    // until playback ends; do nothing here.
                    else -> Unit
                }
            }
            .launchIn(scope)

        pipeJob = repo.playbackCalls.onEach { (call, flag) ->
            // Drop anything that arrives while disconnected — there's a narrow
            // window between closing the socket and the kernel actually
            // flushing it where an in-flight CAL frame can still land.
            val curState = repo.state.value
            if (curState !is ConnectionState.Connected) {
                Log.w(TAG, "pipeJob: dropping CAL id=${call.id} flag=$flag — state is $curState (expected Connected)")
                return@onEach
            }
            val userRequested = flag == FLAG_PLAY
            // User-requested calls (flag "p", from Search/Replay) bypass
            // hold/avoid/livefeed filters — only unsolicited feed calls
            // respect them.
            if (!userRequested) {
                if (!repo.held.value.allows(call)) {
                    Log.d(TAG, "pipeJob: dropping CAL id=${call.id} — hold filter")
                    return@onEach
                }
                if (repo.isAvoided(call.system, call.talkgroup)) {
                    Log.d(TAG, "pipeJob: dropping CAL id=${call.id} — avoided sys=${call.system} tg=${call.talkgroup}")
                    return@onEach
                }
                if (!repo.livefeedEnabled.value) {
                    Log.d(TAG, "pipeJob: dropping CAL id=${call.id} — livefeed disabled")
                    return@onEach
                }
            }
            val labels = resolveLabels(repo.config.value, call)
            val alertBeeps = resolveAlertBeeps(repo.config.value, call)
            if (userRequested) {
                // Switch-now semantics: insert right after the current
                // item and seek to it, so tapping play on a different
                // search row interrupts the currently-playing call
                // instead of queueing the new one behind it.
                callPlayer.playNow(
                    call = call,
                    systemLabel = labels.systemLabel,
                    talkgroupLabel = labels.talkgroupLabel,
                    talkgroupName = labels.talkgroupName,
                    alertBeeps = alertBeeps,
                )
            } else {
                callPlayer.enqueue(
                    call = call,
                    systemLabel = labels.systemLabel,
                    talkgroupLabel = labels.talkgroupLabel,
                    talkgroupName = labels.talkgroupName,
                    alertBeeps = alertBeeps,
                )
            }
        }.launchIn(scope)
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        super.onStartCommand(intent, flags, startId)
        return START_STICKY
    }

    override fun onGetSession(controllerInfo: MediaSession.ControllerInfo): MediaSession? = session

    override fun onDestroy() {
        pipeJob?.cancel()
        foregroundJob?.cancel()
        stopListeningForeground()
        // stopAndClear handles playback halt + queue teardown + cache-file
        // cleanup. Without it, a process-level service kill leaves the
        // ExoPlayer half-alive and audio-cache files on disk.
        callPlayer.stopAndClear()
        session?.release()
        session = null
        scope.cancel()
        super.onDestroy()
    }

    /**
     * Swipe-from-recents means the user closed the app. Stop playback and
     * tear down so the notification disappears and the process can exit.
     * Simply backgrounding the app does not trigger this — audio keeps going.
     */
    override fun onTaskRemoved(rootIntent: Intent?) {
        callPlayer.stopAndClear()
        stopListeningForeground()
        stopSelf()
    }

    private fun ensureListeningChannel() {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.O) return
        val nm = getSystemService<NotificationManager>() ?: return
        if (nm.getNotificationChannel(LISTENING_CHANNEL_ID) != null) return
        val channel = NotificationChannel(
            LISTENING_CHANNEL_ID,
            "Live feed",
            NotificationManager.IMPORTANCE_LOW,
        ).apply {
            description = "Shown while connected to a scanner so audio can keep flowing in the background."
            setShowBadge(false)
            enableLights(false)
            enableVibration(false)
        }
        nm.createNotificationChannel(channel)
    }

    private fun buildListeningNotification(state: ConnectionState): Notification {
        val launch = Intent(this, MainActivity::class.java).apply {
            addFlags(Intent.FLAG_ACTIVITY_SINGLE_TOP or Intent.FLAG_ACTIVITY_CLEAR_TOP)
        }
        val launchPi = PendingIntent.getActivity(
            this, 0, launch,
            PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
        )
        val title = "Rdio Scanner"
        val body = when (state) {
            is ConnectionState.Connected -> "Listening to live feed"
            is ConnectionState.Authenticating -> "Authenticating…"
            is ConnectionState.Connecting -> "Connecting…"
            else -> "Listening to live feed"
        }
        return NotificationCompat.Builder(this, LISTENING_CHANNEL_ID)
            .setContentTitle(title)
            .setContentText(body)
            .setSmallIcon(R.mipmap.ic_launcher)
            .setOngoing(true)
            .setSilent(true)
            .setShowWhen(false)
            .setPriority(NotificationCompat.PRIORITY_LOW)
            .setCategory(NotificationCompat.CATEGORY_SERVICE)
            .setContentIntent(launchPi)
            .build()
    }

    private fun startListeningForeground(state: ConnectionState) {
        val notif = buildListeningNotification(state)
        try {
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) {
                startForeground(
                    LISTENING_NOTIFICATION_ID,
                    notif,
                    ServiceInfo.FOREGROUND_SERVICE_TYPE_MEDIA_PLAYBACK,
                )
            } else {
                @Suppress("DEPRECATION")
                startForeground(LISTENING_NOTIFICATION_ID, notif)
            }
            foregroundActive = true
        } catch (t: Throwable) {
            Log.w(TAG, "startListeningForeground failed: ${t.message}", t)
        }
    }

    private fun stopListeningForeground() {
        if (!foregroundActive) return
        try {
            stopForeground(STOP_FOREGROUND_REMOVE)
        } catch (_: Throwable) {
        }
        foregroundActive = false
    }

    companion object {
        private const val TAG = "AudioService"
        // Different from Media3's playback-notification ID so the
        // "Listening" notification can coexist with — and be replaced by —
        // the player's controls notification without our two startForeground
        // calls fighting over the same slot.
        private const val LISTENING_NOTIFICATION_ID = 2002
        private const val LISTENING_CHANNEL_ID = "rdio_scanner_live_feed"
    }

    private data class ResolvedLabels(
        val systemLabel: String,
        val talkgroupLabel: String,
        val talkgroupName: String?,
    )

    private fun resolveLabels(cfg: ConfigDto?, call: CallDto): ResolvedLabels {
        val system: SystemDto? = cfg?.systems?.firstOrNull { it.id == call.system }
        val tg: TalkgroupDto? = system?.talkgroups?.firstOrNull { it.id == call.talkgroup }
        val sysLabel = system?.label?.ifBlank { null } ?: "System ${call.system}"
        val tgLabel = tg?.label?.ifBlank { null }
            ?: tg?.name?.ifBlank { null }
            ?: "TG ${call.talkgroup}"
        val tgName = tg?.name?.ifBlank { null }
        return ResolvedLabels(sysLabel, tgLabel, tgName)
    }

    /**
     * Looks up the alert preset name assigned to this call's talkgroup, then
     * resolves it against the config's [ConfigDto.alerts] map. Returns null
     * when no alert is assigned, the name is unknown, or the preset is empty.
     */
    private fun resolveAlertBeeps(cfg: ConfigDto?, call: CallDto): List<OscillatorBeep>? {
        val system = cfg?.systems?.firstOrNull { it.id == call.system } ?: return null
        // Talkgroup alert wins; fall back to system-level alert. Matches
        // upstream v7 precedence and the webapp's lookup in
        // RdioScannerService.getCallAlertName.
        val name = system.talkgroups.firstOrNull { it.id == call.talkgroup }
            ?.alert?.takeIf { it.isNotBlank() }
            ?: system.alert?.takeIf { it.isNotBlank() }
            ?: return null
        return cfg.alerts?.get(name)?.takeIf { it.isNotEmpty() }
    }
}
