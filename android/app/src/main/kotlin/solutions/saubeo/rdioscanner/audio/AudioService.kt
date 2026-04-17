package solutions.saubeo.rdioscanner.audio

import android.app.PendingIntent
import android.content.Intent
import androidx.media3.session.MediaSession
import androidx.media3.session.MediaSessionService
import solutions.saubeo.rdioscanner.MainActivity
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.flow.launchIn
import kotlinx.coroutines.flow.onEach
import solutions.saubeo.rdioscanner.RdioApplication
import solutions.saubeo.rdioscanner.data.client.ConnectionState
import solutions.saubeo.rdioscanner.data.protocol.CallDto
import solutions.saubeo.rdioscanner.data.protocol.ConfigDto
import solutions.saubeo.rdioscanner.data.protocol.SystemDto
import solutions.saubeo.rdioscanner.data.protocol.TalkgroupDto
import solutions.saubeo.rdioscanner.data.repository.RdioRepository.Companion.FLAG_PLAY

/**
 * MediaSessionService that owns the [CallPlayer] and bridges server calls
 * from the repository into the playback queue.
 */
class AudioService : MediaSessionService() {
    private lateinit var callPlayer: CallPlayer
    private var session: MediaSession? = null
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.Main.immediate)
    private var pipeJob: Job? = null

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

        val repo = app.repository
        pipeJob = repo.playbackCalls.onEach { (call, flag) ->
            // Drop anything that arrives while disconnected — there's a narrow
            // window between closing the socket and the kernel actually
            // flushing it where an in-flight CAL frame can still land.
            if (repo.state.value !is ConnectionState.Connected) return@onEach
            val userRequested = flag == FLAG_PLAY
            // User-requested calls (flag "p", from Search/Replay) bypass
            // hold/avoid/livefeed filters — only unsolicited feed calls
            // respect them.
            if (!userRequested) {
                if (!repo.held.value.allows(call)) return@onEach
                if (repo.isAvoided(call.system, call.talkgroup)) return@onEach
                if (!repo.livefeedEnabled.value) return@onEach
            }
            val labels = resolveLabels(repo.config.value, call)
            callPlayer.enqueue(
                call = call,
                systemLabel = labels.systemLabel,
                talkgroupLabel = labels.talkgroupLabel,
                talkgroupName = labels.talkgroupName,
            )
        }.launchIn(scope)
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        super.onStartCommand(intent, flags, startId)
        return START_STICKY
    }

    override fun onGetSession(controllerInfo: MediaSession.ControllerInfo): MediaSession? = session

    override fun onDestroy() {
        pipeJob?.cancel()
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
        stopSelf()
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
}
