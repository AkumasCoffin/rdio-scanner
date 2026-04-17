package solutions.saubeo.rdioscanner.data.repository

import android.content.ContentValues
import android.content.Context
import android.net.Uri
import android.os.Build
import android.os.Environment
import android.provider.MediaStore
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.SharedFlow
import kotlinx.coroutines.flow.asSharedFlow
import kotlinx.coroutines.flow.launchIn
import kotlinx.coroutines.flow.onEach
import kotlinx.coroutines.withContext
import solutions.saubeo.rdioscanner.data.protocol.CallDto
import java.io.File
import java.io.FileOutputStream

sealed interface DownloadEvent {
    data class Saved(val fileName: String, val uri: Uri) : DownloadEvent
    data class Failed(val fileName: String, val reason: String) : DownloadEvent
}

class Downloader(
    private val context: Context,
    private val repository: RdioRepository,
) {
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.IO)

    private val _events = MutableSharedFlow<DownloadEvent>(extraBufferCapacity = 8)
    val events: SharedFlow<DownloadEvent> = _events.asSharedFlow()

    init {
        repository.downloadedCalls.onEach { call -> save(call) }.launchIn(scope)
    }

    private suspend fun save(call: CallDto) {
        val fileName = call.audioName?.ifBlank { null } ?: "rdio-call-${call.id}.m4a"
        val mime = call.audioType?.ifBlank { null } ?: "audio/mp4"
        val result = runCatching {
            withContext(Dispatchers.IO) {
                writeToDownloads(fileName, mime, call.audio)
            }
        }
        result.onSuccess { uri ->
            _events.emit(DownloadEvent.Saved(fileName, uri))
        }.onFailure { t ->
            _events.emit(DownloadEvent.Failed(fileName, t.message ?: "unknown"))
        }
    }

    private fun writeToDownloads(name: String, mime: String, bytes: ByteArray): Uri {
        val resolver = context.contentResolver
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) {
            val values = ContentValues().apply {
                put(MediaStore.Downloads.DISPLAY_NAME, name)
                put(MediaStore.Downloads.MIME_TYPE, mime)
                put(MediaStore.Downloads.RELATIVE_PATH, "Download/RdioScanner")
                put(MediaStore.Downloads.IS_PENDING, 1)
            }
            val uri = resolver.insert(MediaStore.Downloads.EXTERNAL_CONTENT_URI, values)
                ?: error("MediaStore refused insert")
            resolver.openOutputStream(uri)?.use { it.write(bytes) }
                ?: error("cannot open output for $uri")
            resolver.update(
                uri,
                ContentValues().apply { put(MediaStore.Downloads.IS_PENDING, 0) },
                null, null,
            )
            return uri
        } else {
            val dir = File(
                @Suppress("DEPRECATION")
                Environment.getExternalStoragePublicDirectory(Environment.DIRECTORY_DOWNLOADS),
                "RdioScanner",
            ).apply { mkdirs() }
            val file = File(dir, name)
            FileOutputStream(file).use { it.write(bytes) }
            return Uri.fromFile(file)
        }
    }

    fun shutdown() {
        scope.cancel()
    }
}
