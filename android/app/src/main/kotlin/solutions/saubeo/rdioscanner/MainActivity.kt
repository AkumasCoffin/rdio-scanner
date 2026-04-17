package solutions.saubeo.rdioscanner

import android.Manifest
import android.content.Intent
import android.content.pm.PackageManager
import android.os.Build
import android.os.Bundle

import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.runtime.CompositionLocalProvider
import androidx.core.content.ContextCompat
import solutions.saubeo.rdioscanner.audio.AudioService
import solutions.saubeo.rdioscanner.ui.LocalClickSound
import solutions.saubeo.rdioscanner.ui.RdioApp
import solutions.saubeo.rdioscanner.ui.theme.RdioTheme

class MainActivity : ComponentActivity() {

    private val requestNotificationPermission =
        registerForActivityResult(ActivityResultContracts.RequestPermission()) { /* noop */ }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        maybeAskNotificationPermission()
        startAudioService()
        val app = application as RdioApplication
        setContent {
            CompositionLocalProvider(LocalClickSound provides app.click) {
                RdioTheme {
                    RdioApp()
                }
            }
        }
    }

    private fun startAudioService() {
        // Plain startService is intentional: MediaSessionService self-promotes to
        // foreground once playback actually starts, avoiding the 5-second
        // startForeground() ANR window when no media is queued yet.
        startService(Intent(this, AudioService::class.java))
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
