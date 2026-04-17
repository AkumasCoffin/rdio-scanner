package solutions.saubeo.rdioscanner.audio

import android.content.Context
import android.net.Uri
import androidx.media3.common.MediaItem
import androidx.media3.common.MediaMetadata
import androidx.media3.common.PlaybackException
import androidx.media3.common.Player
import androidx.media3.exoplayer.ExoPlayer
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import solutions.saubeo.rdioscanner.data.protocol.CallDto
import java.io.File
import java.util.UUID

data class QueuedCall(
    val call: CallDto,
    val file: File,
    val systemLabel: String?,
    val talkgroupLabel: String?,
    val talkgroupName: String? = null,
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

    private val playerListener = object : Player.Listener {
        override fun onMediaItemTransition(mediaItem: MediaItem?, reason: Int) {
            _playing.value?.let { recordHistory(it) }
            updatePlayingAndQueue()
            cleanupPastItems()
        }

        override fun onIsPlayingChanged(isPlaying: Boolean) {
            _isPlaying.value = isPlaying
        }

        override fun onPlaybackStateChanged(playbackState: Int) {
            if (playbackState == Player.STATE_ENDED) {
                _playing.value?.let { recordHistory(it) }
                _playing.value = null
                _queue.value = emptyList()
                cleanupPastItems()
            }
        }

        override fun onPlayerError(error: PlaybackException) {
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
    }

    private fun recordHistory(played: QueuedCall) {
        val next = (_history.value.filterNot { it.call.id == played.call.id }
            .let { listOf(played) + it })
            .take(HISTORY_LIMIT)
        _history.value = next
    }

    val player: ExoPlayer = ExoPlayer.Builder(context)
        .setHandleAudioBecomingNoisy(true)
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
    ) {
        if (call.audio.isEmpty()) return
        val ext = call.audioName?.substringAfterLast('.', "m4a")?.takeIf { it.isNotBlank() } ?: "m4a"
        val file = File(cacheDir, "${UUID.randomUUID()}.$ext")
        file.writeBytes(call.audio)
        val mediaId = file.nameWithoutExtension
        mediaIdToQueued[mediaId] = QueuedCall(call, file, systemLabel, talkgroupLabel, talkgroupName)

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
        val call = queued.call
        if (call.audio.isEmpty()) return
        val ext = call.audioName?.substringAfterLast('.', "m4a")?.takeIf { it.isNotBlank() } ?: "m4a"
        val file = File(cacheDir, "${UUID.randomUUID()}.$ext")
        file.writeBytes(call.audio)
        val mediaId = file.nameWithoutExtension
        mediaIdToQueued[mediaId] = QueuedCall(
            call, file, queued.systemLabel, queued.talkgroupLabel, queued.talkgroupName,
        )

        val item = MediaItem.Builder()
            .setMediaId(mediaId)
            .setUri(Uri.fromFile(file))
            .setMediaMetadata(
                buildCallMetadata(
                    call,
                    queued.systemLabel,
                    queued.talkgroupLabel,
                    queued.talkgroupName,
                )
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
        player.clearMediaItems()
        player.stop()
        _queue.value = emptyList()
        _playing.value = null
        _isPlaying.value = false
        mediaIdToQueued.values.forEach { it.file.delete() }
        mediaIdToQueued.clear()
    }

    fun release() {
        player.removeListener(playerListener)
        player.release()
        cacheDir.listFiles()?.forEach { it.delete() }
        mediaIdToQueued.clear()
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
        const val HISTORY_LIMIT = 8
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
