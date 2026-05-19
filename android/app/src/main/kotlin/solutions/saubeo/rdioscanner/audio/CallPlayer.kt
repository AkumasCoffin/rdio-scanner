package solutions.saubeo.rdioscanner.audio

import android.content.Context
import android.net.Uri
import android.util.Log
import androidx.media3.common.AudioAttributes
import androidx.media3.common.C
import androidx.media3.common.MediaItem
import androidx.media3.common.MediaMetadata
import androidx.media3.common.PlaybackException
import androidx.media3.common.Player
import androidx.media3.exoplayer.ExoPlayer
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import solutions.saubeo.rdioscanner.data.protocol.CallDto
import solutions.saubeo.rdioscanner.data.protocol.OscillatorBeep
import java.io.File
import java.util.UUID

data class QueuedCall(
    val call: CallDto,
    val file: File,
    val systemLabel: String?,
    val talkgroupLabel: String?,
    val talkgroupName: String? = null,
    /** Oscillator preset to play before this call's audio. Null = no alert. */
    val alertBeeps: List<OscillatorBeep>? = null,
)

/**
 * Thin wrapper around ExoPlayer that enqueues rdio-scanner [CallDto] objects
 * as MediaItems backed by cache files. Emits queue + playing state as Flows.
 */
class CallPlayer(private val context: Context) {
    private val cacheDir: File by lazy {
        File(context.cacheDir, "audio-cache").also { it.mkdirs() }
    }

    private val _queue = MutableStateFlow<List<QueuedCall>>(emptyList())
    val queue: StateFlow<List<QueuedCall>> = _queue.asStateFlow()

    private val _playing = MutableStateFlow<QueuedCall?>(null)
    val playing: StateFlow<QueuedCall?> = _playing.asStateFlow()

    private val _isPlaying = MutableStateFlow(false)
    val isPlaying: StateFlow<Boolean> = _isPlaying.asStateFlow()

    private val _history = MutableStateFlow<List<QueuedCall>>(emptyList())
    /** Newest-first ring of the last [HISTORY_LIMIT] played calls. */
    val history: StateFlow<List<QueuedCall>> = _history.asStateFlow()

    private val mediaIdToQueued = HashMap<String, QueuedCall>()

    private val alertPlayer = AlertPlayer()
    private val alertScope = CoroutineScope(SupervisorJob() + Dispatchers.IO)
    private var alertJob: Job? = null
    /** Tracks which mediaId we've already played an alert for, so resuming
     *  a paused item or transitions caused by seek don't re-fire the alert. */
    private val alertedMediaIds = HashSet<String>()

    private val playerListener = object : Player.Listener {
        override fun onMediaItemTransition(mediaItem: MediaItem?, reason: Int) {
            _playing.value?.let { recordHistory(it) }
            updatePlayingAndQueue()
            cleanupPastItems()
            mediaItem?.let { maybePlayAlertFor(it) }
        }

        override fun onIsPlayingChanged(isPlaying: Boolean) {
            _isPlaying.value = isPlaying
        }

        override fun onPlayerError(error: PlaybackException) {
            Log.e(TAG, "onPlayerError: code=${error.errorCode} name=${error.errorCodeName} msg=${error.message}", error)
            // Broken item — advance past it. If it was the last, the STATE_ENDED
            // handler will clean up on its own.
            if (player.hasNextMediaItem()) {
                player.seekToNextMediaItem()
                player.prepare()
                player.playWhenReady = true
                player.play()
            } else {
                player.clearMediaItems()
                player.stop()
            }
        }

        override fun onPlaybackStateChanged(playbackState: Int) {
            if (playbackState == Player.STATE_ENDED) {
                _playing.value?.let { recordHistory(it) }
                _playing.value = null
                _queue.value = emptyList()
                cleanupPastItems()
            }
        }
    }

    private fun recordHistory(played: QueuedCall) {
        val next = (_history.value.filterNot { it.call.id == played.call.id }
            .let { listOf(played) + it })
            .take(HISTORY_LIMIT)
        _history.value = next
    }

    val player: ExoPlayer = ExoPlayer.Builder(context)
        .setHandleAudioBecomingNoisy(true)
        // Without explicit attributes + handleAudioFocus, the player doesn't
        // duck for nav prompts or pause for phone calls, and some OEM media
        // surfaces won't recognise it as media-playback worth showing on
        // lock screen / Bluetooth displays. SPEECH is the right content
        // type for scanner traffic.
        .setAudioAttributes(
            AudioAttributes.Builder()
                .setUsage(C.USAGE_MEDIA)
                .setContentType(C.AUDIO_CONTENT_TYPE_SPEECH)
                .build(),
            /* handleAudioFocus = */ true,
        )
        .build()
        .apply {
            playWhenReady = true
            addListener(playerListener)
        }

    fun enqueue(
        call: CallDto,
        systemLabel: String?,
        talkgroupLabel: String?,
        talkgroupName: String? = null,
        alertBeeps: List<OscillatorBeep>? = null,
    ) {
        if (call.audio.isEmpty()) {
            Log.w(TAG, "enqueue: id=${call.id} audio bytes empty — skipping")
            return
        }
        val ext = call.audioName?.substringAfterLast('.', "m4a")?.takeIf { it.isNotBlank() } ?: "m4a"
        val file = File(cacheDir, "${UUID.randomUUID()}.$ext")
        file.writeBytes(call.audio)
        val mediaId = file.nameWithoutExtension
        mediaIdToQueued[mediaId] = QueuedCall(call, file, systemLabel, talkgroupLabel, talkgroupName, alertBeeps)

        val item = MediaItem.Builder()
            .setMediaId(mediaId)
            .setUri(Uri.fromFile(file))
            .setMediaMetadata(buildCallMetadata(call, systemLabel, talkgroupLabel, talkgroupName))
            .build()
        player.addMediaItem(item)
        // After STATE_ENDED, calling play() alone does not resume — you have
        // to seek out of the ended position first. After STATE_IDLE (post
        // stop/clear), re-prepare. Either state needs explicit recovery or
        // the queue looks stuck.
        when (player.playbackState) {
            Player.STATE_IDLE -> player.prepare()
            Player.STATE_ENDED -> {
                val newIdx = player.mediaItemCount - 1
                if (newIdx >= 0) player.seekTo(newIdx, 0)
            }
        }
        player.playWhenReady = true
        if (!player.isPlaying) player.play()
        updatePlayingAndQueue()
    }

    fun playPause() {
        if (player.isPlaying) player.pause() else player.play()
    }

    /**
     * Matches the webapp `replay()` — interrupts the current call and plays the
     * supplied one immediately (or queues at the head if nothing is playing).
     */
    fun replay(queued: QueuedCall) {
        playNow(
            call = queued.call,
            systemLabel = queued.systemLabel,
            talkgroupLabel = queued.talkgroupLabel,
            talkgroupName = queued.talkgroupName,
            alertBeeps = queued.alertBeeps,
        )
    }

    /**
     * Inserts [call] immediately after the currently-playing item and
     * seeks playback to it, so a user-initiated play interrupts whatever's
     * already going. Used by the Search-screen play button and the LCD's
     * Replay action — both need switch-now semantics rather than the
     * append-and-wait-for-current-to-end behavior of [enqueue].
     */
    fun playNow(
        call: CallDto,
        systemLabel: String?,
        talkgroupLabel: String?,
        talkgroupName: String? = null,
        alertBeeps: List<OscillatorBeep>? = null,
    ) {
        if (call.audio.isEmpty()) {
            Log.w(TAG, "playNow: id=${call.id} audio bytes empty — skipping")
            return
        }
        val ext = call.audioName?.substringAfterLast('.', "m4a")?.takeIf { it.isNotBlank() } ?: "m4a"
        val file = File(cacheDir, "${UUID.randomUUID()}.$ext")
        file.writeBytes(call.audio)
        val mediaId = file.nameWithoutExtension
        mediaIdToQueued[mediaId] = QueuedCall(
            call, file, systemLabel, talkgroupLabel, talkgroupName, alertBeeps,
        )

        val item = MediaItem.Builder()
            .setMediaId(mediaId)
            .setUri(Uri.fromFile(file))
            .setMediaMetadata(
                buildCallMetadata(call, systemLabel, talkgroupLabel, talkgroupName)
            )
            .build()

        val count = player.mediaItemCount
        val insertAt = if (count == 0) 0 else player.currentMediaItemIndex.coerceAtLeast(0) + 1
        player.addMediaItem(insertAt, item)

        if (player.playbackState == Player.STATE_IDLE) player.prepare()
        player.seekTo(insertAt, 0)
        player.playWhenReady = true
        player.play()
        updatePlayingAndQueue()
    }

    fun skip() {
        if (player.hasNextMediaItem()) player.seekToNextMediaItem() else stopAndClear()
    }

    fun stopAndClear() {
        // pause() first — stop() on ExoPlayer resets state but can let the
        // current audio buffer finish draining into AudioTrack. pause flips
        // playWhenReady immediately so the output sink drops sample feeding.
        player.playWhenReady = false
        player.pause()
        // clearMediaItems() synchronously fires onMediaItemTransition with a
        // null item, so the listener drives _playing/_isPlaying down to a
        // consistent state. Doing it before stop() also avoids a stray
        // late onIsPlayingChanged(false) overwriting a fresh playNow that
        // lands between stop() and the listener — which was making the
        // Search row Play/Stop icon flicker on rapid stop-then-play. The
        // listener is the single writer for these flows; do not poke them
        // from here.
        player.clearMediaItems()
        player.stop()
        _queue.value = emptyList()
        mediaIdToQueued.values.forEach { it.file.delete() }
        mediaIdToQueued.clear()
    }

    /**
     * Drops the played-history ring. Distinct from [stopAndClear] because
     * toggling livefeed off is supposed to preserve history (the user may
     * still want to see what just played), but a profile switch is supposed
     * to wipe it — system / talkgroup ids only mean anything for the server
     * the calls came from.
     */
    fun clearHistory() {
        _history.value = emptyList()
    }

    fun release() {
        alertJob?.cancel()
        alertScope.cancel()
        player.removeListener(playerListener)
        player.release()
        cacheDir.listFiles()?.forEach { it.delete() }
        mediaIdToQueued.clear()
        alertedMediaIds.clear()
    }

    /**
     * When ExoPlayer transitions to a new media item with an attached alert
     * preset, pauses playback, plays the alert synchronously on IO, and
     * resumes. Each mediaId is alerted at most once so seeks / pause-resume
     * don't replay the tone.
     */
    private fun maybePlayAlertFor(mediaItem: MediaItem) {
        val mediaId = mediaItem.mediaId ?: return
        if (mediaId in alertedMediaIds) return
        val queued = mediaIdToQueued[mediaId] ?: return
        val beeps = queued.alertBeeps?.takeIf { it.isNotEmpty() } ?: return

        alertedMediaIds.add(mediaId)
        player.playWhenReady = false
        alertJob?.cancel()
        alertJob = alertScope.launch {
            try {
                alertPlayer.play(beeps)
            } catch (t: Throwable) {
                Log.w(TAG, "alert preset playback failed: ${t.message}")
            }
            withContext(Dispatchers.Main) {
                // If the user switched calls or stopped while the alert was
                // playing, don't yank playback back to true.
                val currentId = currentMediaItemId()
                if (currentId == mediaId) {
                    player.playWhenReady = true
                    if (!player.isPlaying) player.play()
                }
            }
        }
    }

    private fun currentMediaItemId(): String? {
        val count = player.mediaItemCount
        if (count == 0) return null
        val idx = player.currentMediaItemIndex.coerceIn(0, count - 1)
        return player.getMediaItemAt(idx).mediaId
    }

    private fun updatePlayingAndQueue() {
        val count = player.mediaItemCount
        if (count == 0) {
            _playing.value = null
            _queue.value = emptyList()
            return
        }
        val currentIndex = player.currentMediaItemIndex.coerceIn(0, count - 1)
        val currentId = player.getMediaItemAt(currentIndex).mediaId
        _playing.value = mediaIdToQueued[currentId]

        val upcoming = buildList {
            for (i in (currentIndex + 1) until count) {
                val id = player.getMediaItemAt(i).mediaId
                mediaIdToQueued[id]?.let { add(it) }
            }
        }
        _queue.value = upcoming
    }

    companion object {
        const val HISTORY_LIMIT = 100
        private const val TAG = "CallPlayer"
    }

    /**
     * Metadata used by Android's media notification and lock-screen controls.
     * Title carries the talkgroup name (or label), artist carries the system
     * so both appear on the system-level player surface.
     */
    private fun buildCallMetadata(
        call: CallDto,
        systemLabel: String?,
        talkgroupLabel: String?,
        talkgroupName: String?,
    ): MediaMetadata {
        val sys = systemLabel?.ifBlank { null } ?: "System ${call.system}"
        val tgLabel = talkgroupLabel?.ifBlank { null } ?: "TG ${call.talkgroup}"
        val tgName = talkgroupName?.ifBlank { null }
        // Primary title: the descriptive name if we have one, else the short
        // label. Matches the webapp's big-row `callTalkgroupName`.
        val title = tgName ?: tgLabel
        // Subtitle mirrors the webapp's system · talkgroup status line.
        val subtitle = if (tgName != null && tgName != tgLabel) "$sys  ·  $tgLabel" else sys
        return MediaMetadata.Builder()
            .setTitle(title)
            .setDisplayTitle(title)
            .setArtist(sys)
            .setSubtitle(subtitle)
            .setAlbumTitle(sys)
            .setIsPlayable(true)
            .setIsBrowsable(false)
            .setMediaType(MediaMetadata.MEDIA_TYPE_MUSIC)
            .build()
    }

    /** Delete files for items that sit before the current playback index. */
    private fun cleanupPastItems() {
        val count = player.mediaItemCount
        if (count == 0) return
        val currentIndex = player.currentMediaItemIndex.coerceAtLeast(0)
        val liveIds = buildSet {
            for (i in currentIndex until count) add(player.getMediaItemAt(i).mediaId)
        }
        val iter = mediaIdToQueued.entries.iterator()
        while (iter.hasNext()) {
            val entry = iter.next()
            if (entry.key !in liveIds) {
                entry.value.file.delete()
                iter.remove()
            }
        }
    }
}
