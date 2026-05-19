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
 * Foreground lifecycle: the activity-side Composable fires
 * [ACTION_ENTER_FG] via `Context.startForegroundService()` from a foreground
 * context (its `LaunchedEffect` only runs while the activity is composing,
 * which guarantees a TOP state); this service responds in [onStartCommand]
 * by calling `startForeground()` with the "Listening to live feed"
 * notification. Once promoted, we keep the service foreground until the
 * activity fires [ACTION_EXIT_FG] (user-initiated disconnect) or the
 * service is destroyed.
 *
 * The WS-state observer in this service NEVER calls `startForeground`
 * directly — Android 12+ refuses to promote a service to foreground from
 * a background coroutine (`mAllowStartForeground = false`), which we hit
 * hard on Samsung Android 16. Instead, the observer just refreshes the
 * notification text via [NotificationManager.notify], which works from
 * any context as long as the notification ID already exists.
 *
 * Media3 manages its own playback notification while a call plays. After
 * playback ends our service should still be foreground because we never
 * called `stopForeground` — Media3 only detaches its own notification id.
 */
class AudioService : MediaSessionService() {
    private lateinit var callPlayer: CallPlayer
    private var session: MediaSession? = null
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.Main.immediate)
    private var pipeJob: Job? = null
    private var notificationJob: Job? = null
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

        // Refresh the notification text when the WS state changes. This is
        // notify-only — never startForeground from here (we'd hit
        // mAllowStartForeground = false on background coroutines and the
        // resulting ForegroundServiceStartNotAllowedException would crash
        // a debug build). Promotion happens via ACTION_ENTER_FG from the UI
        // layer's LaunchedEffect, which is guaranteed to fire while the
        // activity is composing — i.e. foreground.
        notificationJob = combine(
            repo.state,
            callPlayer.isPlaying,
        ) { state, playing -> state to playing }
            .distinctUntilChanged()
            .onEach { (state, playing) ->
                if (foregroundActive && !playing) {
                    refreshListeningNotification(state)
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
        // Handle our own actions before delegating to MediaSessionService. The
        // app-side Composable fires ACTION_ENTER_FG via
        // Context.startForegroundService() from a foreground context (the
        // LaunchedEffect inside RdioApp), which is the only safe place to
        // promote ourselves to foreground on Android 12+.
        val action = intent?.action
        Log.d(TAG, "onStartCommand: action=$action flags=$flags startId=$startId foregroundActive=$foregroundActive")
        when (action) {
            ACTION_ENTER_FG -> {
                val app = application as RdioApplication
                val state = app.repository.state.value
                startListeningForeground(state)
            }
            ACTION_EXIT_FG -> stopListeningForeground()
        }
        super.onStartCommand(intent, flags, startId)
        return START_STICKY
    }

    override fun onGetSession(controllerInfo: MediaSession.ControllerInfo): MediaSession? = session

    override fun onDestroy() {
        pipeJob?.cancel()
        notificationJob?.cancel()
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
            Log.i(TAG, "startListeningForeground: OK state=$state notifId=$LISTENING_NOTIFICATION_ID")
        } catch (t: Throwable) {
            // Will hit on Android 12+ if the caller wasn't actually in a
            // foreground context when sending ACTION_ENTER_FG. Logged so the
            // failure is debuggable, not crashed — the WS still runs as a
            // background process; it just won't survive Doze.
            Log.w(TAG, "startListeningForeground FAILED state=$state: ${t.message}", t)
        }
    }

    private fun stopListeningForeground() {
        if (!foregroundActive) {
            Log.d(TAG, "stopListeningForeground: skip — not currently foreground")
            return
        }
        try {
            stopForeground(STOP_FOREGROUND_REMOVE)
            Log.i(TAG, "stopListeningForeground: OK")
        } catch (t: Throwable) {
            Log.w(TAG, "stopListeningForeground FAILED: ${t.message}", t)
        }
        foregroundActive = false
    }

    /** Updates the existing Listening notification's text in place. Safe to
     *  call from any context — NotificationManager.notify does not require
     *  the FGS-launch exemption that startForeground does. No-op if we
     *  aren't actually foreground (no notification to update). */
    private fun refreshListeningNotification(state: ConnectionState) {
        val nm = getSystemService<NotificationManager>() ?: return
        val notif = buildListeningNotification(state)
        try {
            nm.notify(LISTENING_NOTIFICATION_ID, notif)
        } catch (_: Throwable) {
        }
    }

    companion object {
        private const val TAG = "AudioService"
        // Different from Media3's playback-notification ID so the
        // "Listening" notification can coexist with — and be replaced by —
        // the player's controls notification without our two startForeground
        // calls fighting over the same slot.
        private const val LISTENING_NOTIFICATION_ID = 2002
        private const val LISTENING_CHANNEL_ID = "rdio_scanner_live_feed"
        /** Intent action: promote the service to foreground with the
         *  Listening notification. MUST be sent via
         *  Context.startForegroundService() from a foreground context
         *  (e.g. a Composable's LaunchedEffect). */
        const val ACTION_ENTER_FG = "solutions.saubeo.rdioscanner.action.ENTER_FG"
        /** Intent action: demote the service from foreground. Safe to send
         *  from any context — stopForeground doesn't need the FGS-launch
         *  exemption. */
        const val ACTION_EXIT_FG = "solutions.saubeo.rdioscanner.action.EXIT_FG"
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
