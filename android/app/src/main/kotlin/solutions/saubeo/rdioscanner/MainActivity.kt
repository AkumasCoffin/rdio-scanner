package solutions.saubeo.rdioscanner

import android.Manifest
import android.content.ComponentName
import android.content.Intent
import android.content.pm.PackageManager
import android.os.Build
import android.os.Bundle

import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.runtime.CompositionLocalProvider
import androidx.core.content.ContextCompat
import androidx.media3.session.MediaController
import androidx.media3.session.SessionToken
import com.google.common.util.concurrent.ListenableFuture
import solutions.saubeo.rdioscanner.audio.AudioService
import solutions.saubeo.rdioscanner.ui.LocalClickSound
import solutions.saubeo.rdioscanner.ui.RdioApp
import solutions.saubeo.rdioscanner.ui.theme.RdioTheme

class MainActivity : ComponentActivity() {

    private val requestNotificationPermission =
        registerForActivityResult(ActivityResultContracts.RequestPermission()) { /* noop */ }

    private var controllerFuture: ListenableFuture<MediaController>? = null

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        maybeAskNotificationPermission()
        startAudioService()
        bindMediaController()
        val app = application as RdioApplication
        setContent {
            CompositionLocalProvider(LocalClickSound provides app.click) {
                RdioTheme {
                    RdioApp()
                }
            }
        }
    }

    override fun onDestroy() {
        controllerFuture?.let { MediaController.releaseFuture(it) }
        controllerFuture = null
        super.onDestroy()
    }

    private fun startAudioService() {
        // Plain startService is intentional: MediaSessionService self-promotes to
        // foreground once playback actually starts, avoiding the 5-second
        // startForeground() ANR window when no media is queued yet.
        startService(Intent(this, AudioService::class.java))
    }

    // MediaSessionService only posts its system media notification when a
    // MediaController connects to the session — without this binding the
    // service never calls startForeground() so the notification panel,
    // lock screen, and Quick Settings tile stay blank even though audio
    // is playing. We don't actually use the controller for playback (the
    // UI still drives CallPlayer directly), it just exists to trigger
    // the notification flow.
    private fun bindMediaController() {
        val token = SessionToken(this, ComponentName(this, AudioService::class.java))
        controllerFuture = MediaController.Builder(this, token).buildAsync()
    }

    private fun maybeAskNotificationPermission() {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.TIRAMISU) return
        val granted = ContextCompat.checkSelfPermission(
            this,
            Manifest.permission.POST_NOTIFICATIONS,
        ) == PackageManager.PERMISSION_GRANTED
        if (!granted) requestNotificationPermission.launch(Manifest.permission.POST_NOTIFICATIONS)
    }
}
