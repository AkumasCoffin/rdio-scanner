package solutions.saubeo.rdioscanner.audio

import android.app.PendingIntent
import android.content.Intent
import android.os.Bundle
import androidx.media3.common.MediaItem
import androidx.media3.common.MediaMetadata
import androidx.media3.session.CommandButton
import androidx.media3.session.LibraryResult
import androidx.media3.session.MediaLibraryService
import androidx.media3.session.MediaSession
import androidx.media3.session.SessionCommand
import androidx.media3.session.SessionResult
import com.google.common.collect.ImmutableList
import com.google.common.util.concurrent.Futures
import com.google.common.util.concurrent.ListenableFuture
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.flow.launchIn
import kotlinx.coroutines.flow.onEach
import solutions.saubeo.rdioscanner.MainActivity
import solutions.saubeo.rdioscanner.R
import solutions.saubeo.rdioscanner.RdioApplication
import solutions.saubeo.rdioscanner.data.client.ConnectionState
import solutions.saubeo.rdioscanner.data.protocol.CallDto
import solutions.saubeo.rdioscanner.data.protocol.ConfigDto
import solutions.saubeo.rdioscanner.data.protocol.SystemDto
import solutions.saubeo.rdioscanner.data.protocol.TalkgroupDto
import solutions.saubeo.rdioscanner.data.repository.RdioRepository
import solutions.saubeo.rdioscanner.data.repository.RdioRepository.Companion.FLAG_PLAY

/**
 * MediaLibraryService that owns the [CallPlayer], pipes server calls into
 * playback, and exposes a browse hierarchy to external surfaces (Android
 * Auto, Wear, Assistant). Extending MediaLibraryService instead of plain
 * MediaSessionService is what lets AA enumerate browse roots — without it
 * AA only sees the now-playing transport.
 */
class AudioService : MediaLibraryService() {
    private lateinit var callPlayer: CallPlayer
    private lateinit var repo: RdioRepository
    private var session: MediaLibrarySession? = null
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.Main.immediate)
    private var pipeJob: Job? = null
    private var transcriptJob: Job? = null
    private val appliedTranscripts = HashMap<Long, String>()

    override fun onCreate() {
        super.onCreate()
        val app = application as RdioApplication
        callPlayer = app.audio
        repo = app.repository
        val launch = Intent(this, MainActivity::class.java).apply {
            addFlags(Intent.FLAG_ACTIVITY_SINGLE_TOP or Intent.FLAG_ACTIVITY_CLEAR_TOP)
        }
        val launchPi = PendingIntent.getActivity(
            this, 0, launch,
            PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
        )
        session = MediaLibrarySession.Builder(this, callPlayer.player, libraryCallback)
            .setSessionActivity(launchPi)
            .build()

        pipeJob = repo.playbackCalls.onEach { (call, flag) ->
            // Drop anything that arrives while disconnected — there's a narrow
            // window between closing the socket and the kernel actually
            // flushing it where an in-flight CAL frame can still land.
            if (repo.state.value !is ConnectionState.Connected) return@onEach
            val userRequested = flag == FLAG_PLAY
            // User-requested calls (flag "p", from Search/Replay/AA) bypass
            // hold/avoid/livefeed filters — only unsolicited feed calls
            // respect them.
            if (!userRequested) {
                if (!repo.held.value.allows(call)) return@onEach
                if (repo.isAvoided(call.system, call.talkgroup)) return@onEach
                if (!repo.livefeedEnabled.value) return@onEach
            }
            val labels = resolveLabels(repo.config.value, call)
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
                )
            } else {
                callPlayer.enqueue(
                    call = call,
                    systemLabel = labels.systemLabel,
                    talkgroupLabel = labels.talkgroupLabel,
                    talkgroupName = labels.talkgroupName,
                )
            }
        }.launchIn(scope)

        // Late-arriving Whisper results come in via the TRX channel as
        // (callId → text) map updates. Diff against what we've already
        // applied so we only call into the player for genuinely new
        // entries — the map gets re-emitted on unrelated TRX traffic too.
        transcriptJob = repo.transcripts.onEach { map ->
            for ((id, text) in map) {
                if (appliedTranscripts[id] == text) continue
                appliedTranscripts[id] = text
                callPlayer.applyTranscript(id, text)
            }
        }.launchIn(scope)
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        super.onStartCommand(intent, flags, startId)
        return START_STICKY
    }

    override fun onGetSession(controllerInfo: MediaSession.ControllerInfo): MediaLibrarySession? =
        session

    override fun onDestroy() {
        pipeJob?.cancel()
        transcriptJob?.cancel()
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

    // ──────────────────────────────────────────────────────────────────────
    // MediaLibrarySession.Callback — browse tree + custom commands
    // ──────────────────────────────────────────────────────────────────────

    private val libraryCallback = object : MediaLibrarySession.Callback {
        override fun onConnect(
            session: MediaSession,
            controller: MediaSession.ControllerInfo,
        ): MediaSession.ConnectionResult {
            val sessionCommands = MediaSession.ConnectionResult.DEFAULT_SESSION_COMMANDS
                .buildUpon()
                .add(SessionCommand(CMD_TOGGLE_LIVEFEED, Bundle.EMPTY))
                .add(SessionCommand(CMD_RELEASE_HOLD, Bundle.EMPTY))
                .add(SessionCommand(CMD_REPLAY_LAST, Bundle.EMPTY))
                .build()
            return MediaSession.ConnectionResult.AcceptedResultBuilder(session)
                .setAvailableSessionCommands(sessionCommands)
                .setCustomLayout(customLayout())
                .build()
        }

        override fun onCustomCommand(
            session: MediaSession,
            controller: MediaSession.ControllerInfo,
            customCommand: SessionCommand,
            args: Bundle,
        ): ListenableFuture<SessionResult> {
            when (customCommand.customAction) {
                CMD_TOGGLE_LIVEFEED -> repo.setLivefeedEnabled(!repo.livefeedEnabled.value)
                CMD_RELEASE_HOLD -> repo.releaseHold()
                CMD_REPLAY_LAST -> callPlayer.history.value.firstOrNull()?.let { callPlayer.replay(it) }
            }
            return Futures.immediateFuture(SessionResult(SessionResult.RESULT_SUCCESS))
        }

        override fun onGetLibraryRoot(
            session: MediaLibrarySession,
            browser: MediaSession.ControllerInfo,
            params: LibraryParams?,
        ): ListenableFuture<LibraryResult<MediaItem>> =
            Futures.immediateFuture(LibraryResult.ofItem(buildRoot(), params))

        override fun onGetChildren(
            session: MediaLibrarySession,
            browser: MediaSession.ControllerInfo,
            parentId: String,
            page: Int,
            pageSize: Int,
            params: LibraryParams?,
        ): ListenableFuture<LibraryResult<ImmutableList<MediaItem>>> {
            val items = childrenFor(parentId)
            return Futures.immediateFuture(LibraryResult.ofItemList(items, params))
        }

        override fun onGetItem(
            session: MediaLibrarySession,
            browser: MediaSession.ControllerInfo,
            mediaId: String,
        ): ListenableFuture<LibraryResult<MediaItem>> {
            val item = findItem(mediaId)
            return if (item != null) {
                Futures.immediateFuture(LibraryResult.ofItem(item, /* params = */ null))
            } else {
                Futures.immediateFuture(LibraryResult.ofError(LibraryResult.RESULT_ERROR_BAD_VALUE))
            }
        }

        /**
         * Called when a controller (AA/Wear/MediaController) issues
         * setMediaItems/addMediaItems with our catalog items. Cached call
         * audio is purged after playback so we can't hand back a real URI —
         * instead, fire a server-side replay request and return empty.
         * The CAL frame that comes back rides the existing pipeJob and
         * calls into [CallPlayer.playNow], which is what populates the
         * player's queue.
         */
        override fun onAddMediaItems(
            mediaSession: MediaSession,
            controller: MediaSession.ControllerInfo,
            mediaItems: MutableList<MediaItem>,
        ): ListenableFuture<MutableList<MediaItem>> {
            for (item in mediaItems) {
                val id = item.mediaId
                if (id.startsWith(PREFIX_CALL)) {
                    id.removePrefix(PREFIX_CALL).toLongOrNull()
                        ?.let { repo.requestCall(it, FLAG_PLAY) }
                }
            }
            return Futures.immediateFuture(mutableListOf())
        }
    }

    private fun customLayout(): ImmutableList<CommandButton> = ImmutableList.of(
        CommandButton.Builder()
            .setDisplayName("Replay last")
            .setSessionCommand(SessionCommand(CMD_REPLAY_LAST, Bundle.EMPTY))
            .setIconResId(R.drawable.ic_replay)
            .build(),
        CommandButton.Builder()
            .setDisplayName("Toggle livefeed")
            .setSessionCommand(SessionCommand(CMD_TOGGLE_LIVEFEED, Bundle.EMPTY))
            .setIconResId(R.drawable.ic_livefeed)
            .build(),
        CommandButton.Builder()
            .setDisplayName("Release hold")
            .setSessionCommand(SessionCommand(CMD_RELEASE_HOLD, Bundle.EMPTY))
            .setIconResId(R.drawable.ic_lock_open)
            .build(),
    )

    // ──────────────────────────────────────────────────────────────────────
    // Browse tree
    // ──────────────────────────────────────────────────────────────────────

    private fun buildRoot(): MediaItem = browsable(ID_ROOT, "Rdio Scanner")

    private fun childrenFor(parentId: String): ImmutableList<MediaItem> {
        val cfg = repo.config.value
        return when {
            parentId == ID_ROOT -> ImmutableList.of(
                browsable(ID_LIVE, "Live"),
                browsable(ID_RECENT, "Recent calls"),
                browsable(ID_SYSTEMS, "Systems"),
                browsable(ID_AVOIDED, "Avoided"),
            )
            parentId == ID_LIVE -> buildList {
                callPlayer.playing.value?.let { add(callItem(it)) }
                callPlayer.queue.value.forEach { add(callItem(it)) }
            }.toImmutable()
            parentId == ID_RECENT ->
                callPlayer.history.value.map { callItem(it) }.toImmutable()
            parentId == ID_SYSTEMS ->
                cfg?.systems.orEmpty().map { systemItem(it) }.toImmutable()
            parentId.startsWith(PREFIX_SYSTEM) -> {
                val sysId = parentId.removePrefix(PREFIX_SYSTEM).toIntOrNull()
                val sys = cfg?.systems?.firstOrNull { it.id == sysId }
                sys?.talkgroups.orEmpty().map { tgItem(sys!!.id, it) }.toImmutable()
            }
            parentId.startsWith(PREFIX_TG) -> {
                val (sysId, tgId) = parseTgKey(parentId) ?: return ImmutableList.of()
                callPlayer.history.value
                    .filter { it.call.system == sysId && it.call.talkgroup == tgId }
                    .map { callItem(it) }
                    .toImmutable()
            }
            parentId == ID_AVOIDED -> {
                val avoided = repo.avoided.value
                avoided.mapNotNull { (sysId, tgId) ->
                    val sys = cfg?.systems?.firstOrNull { it.id == sysId } ?: return@mapNotNull null
                    val tg = sys.talkgroups.firstOrNull { it.id == tgId } ?: return@mapNotNull null
                    tgItem(sys.id, tg)
                }.toImmutable()
            }
            else -> ImmutableList.of()
        }
    }

    private fun findItem(mediaId: String): MediaItem? {
        val cfg = repo.config.value
        return when {
            mediaId == ID_ROOT -> buildRoot()
            mediaId == ID_LIVE -> browsable(ID_LIVE, "Live")
            mediaId == ID_RECENT -> browsable(ID_RECENT, "Recent calls")
            mediaId == ID_SYSTEMS -> browsable(ID_SYSTEMS, "Systems")
            mediaId == ID_AVOIDED -> browsable(ID_AVOIDED, "Avoided")
            mediaId.startsWith(PREFIX_SYSTEM) -> {
                val sysId = mediaId.removePrefix(PREFIX_SYSTEM).toIntOrNull()
                cfg?.systems?.firstOrNull { it.id == sysId }?.let { systemItem(it) }
            }
            mediaId.startsWith(PREFIX_TG) -> {
                val (sysId, tgId) = parseTgKey(mediaId) ?: return null
                val sys = cfg?.systems?.firstOrNull { it.id == sysId } ?: return null
                val tg = sys.talkgroups.firstOrNull { it.id == tgId } ?: return null
                tgItem(sys.id, tg)
            }
            mediaId.startsWith(PREFIX_CALL) -> {
                val callId = mediaId.removePrefix(PREFIX_CALL).toLongOrNull() ?: return null
                callPlayer.history.value.firstOrNull { it.call.id == callId }?.let { callItem(it) }
                    ?: callPlayer.queue.value.firstOrNull { it.call.id == callId }?.let { callItem(it) }
                    ?: callPlayer.playing.value?.takeIf { it.call.id == callId }?.let { callItem(it) }
            }
            else -> null
        }
    }

    private fun browsable(id: String, title: String): MediaItem {
        val meta = MediaMetadata.Builder()
            .setTitle(title)
            .setIsBrowsable(true)
            .setIsPlayable(false)
            .setMediaType(MediaMetadata.MEDIA_TYPE_FOLDER_MIXED)
            .build()
        return MediaItem.Builder().setMediaId(id).setMediaMetadata(meta).build()
    }

    private fun systemItem(sys: SystemDto): MediaItem {
        val meta = MediaMetadata.Builder()
            .setTitle(sys.label.ifBlank { "System ${sys.id}" })
            .setIsBrowsable(true)
            .setIsPlayable(false)
            .setMediaType(MediaMetadata.MEDIA_TYPE_FOLDER_MIXED)
            .build()
        return MediaItem.Builder().setMediaId("$PREFIX_SYSTEM${sys.id}").setMediaMetadata(meta).build()
    }

    private fun tgItem(systemId: Int, tg: TalkgroupDto): MediaItem {
        val label = tg.label.ifBlank { null } ?: tg.name.ifBlank { null } ?: "TG ${tg.id}"
        val name = tg.name.ifBlank { null }
        val meta = MediaMetadata.Builder()
            .setTitle(name ?: label)
            .setSubtitle(if (name != null && name != label) label else null)
            .setIsBrowsable(true)
            .setIsPlayable(false)
            .setMediaType(MediaMetadata.MEDIA_TYPE_FOLDER_MIXED)
            .build()
        return MediaItem.Builder()
            .setMediaId("$PREFIX_TG$systemId:${tg.id}")
            .setMediaMetadata(meta)
            .build()
    }

    private fun callItem(queued: QueuedCall): MediaItem {
        val sys = queued.systemLabel ?: "System ${queued.call.system}"
        val tg = queued.talkgroupName?.ifBlank { null }
            ?: queued.talkgroupLabel
            ?: "TG ${queued.call.talkgroup}"
        val transcript = queued.call.transcript?.trim()?.takeIf { it.isNotEmpty() }
        val meta = MediaMetadata.Builder()
            .setTitle(tg)
            .setArtist(sys)
            .setSubtitle(sys)
            .setAlbumTitle(transcript ?: queued.talkgroupLabel)
            .apply { transcript?.let { setDescription(it) } }
            .setIsBrowsable(false)
            .setIsPlayable(true)
            .setMediaType(MediaMetadata.MEDIA_TYPE_RADIO_STATION)
            .build()
        return MediaItem.Builder()
            .setMediaId("$PREFIX_CALL${queued.call.id}")
            .setMediaMetadata(meta)
            .build()
    }

    private fun parseTgKey(id: String): Pair<Int, Int>? {
        val body = id.removePrefix(PREFIX_TG).split(':')
        if (body.size != 2) return null
        val s = body[0].toIntOrNull() ?: return null
        val t = body[1].toIntOrNull() ?: return null
        return s to t
    }

    private fun <T> List<T>.toImmutable(): ImmutableList<T> = ImmutableList.copyOf(this)

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

    companion object {
        private const val ID_ROOT = "root"
        private const val ID_LIVE = "live"
        private const val ID_RECENT = "recent"
        private const val ID_SYSTEMS = "systems"
        private const val ID_AVOIDED = "avoided"
        private const val PREFIX_SYSTEM = "system:"
        private const val PREFIX_TG = "tg:"
        private const val PREFIX_CALL = "call:"

        private const val CMD_TOGGLE_LIVEFEED = "solutions.saubeo.rdioscanner.TOGGLE_LIVEFEED"
        private const val CMD_RELEASE_HOLD = "solutions.saubeo.rdioscanner.RELEASE_HOLD"
        private const val CMD_REPLAY_LAST = "solutions.saubeo.rdioscanner.REPLAY_LAST"
    }
}
