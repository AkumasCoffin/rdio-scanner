package solutions.saubeo.rdioscanner

import android.app.Application
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.flow.launchIn
import kotlinx.coroutines.flow.onEach
import solutions.saubeo.rdioscanner.audio.CallPlayer
import solutions.saubeo.rdioscanner.audio.ClickSound
import solutions.saubeo.rdioscanner.data.client.NetworkMonitor
import solutions.saubeo.rdioscanner.data.client.RdioClient
import solutions.saubeo.rdioscanner.data.prefs.SettingsStore
import solutions.saubeo.rdioscanner.data.repository.HoldState
import solutions.saubeo.rdioscanner.data.repository.Downloader
import solutions.saubeo.rdioscanner.data.repository.RdioRepository

class RdioApplication : Application() {
    lateinit var settings: SettingsStore
        private set
    lateinit var repository: RdioRepository
        private set
    lateinit var audio: CallPlayer
        private set
    lateinit var click: ClickSound
        private set
    lateinit var downloader: Downloader
        private set

    private val appScope = CoroutineScope(SupervisorJob() + Dispatchers.Main.immediate)

    override fun onCreate() {
        super.onCreate()
        settings = SettingsStore(applicationContext)
        // NetworkMonitor lets the WS reconnect path wait for the system
        // to actually have an internet-capable network before retrying —
        // fixes the post-resume "UNABLE TO RESOLVE HOST" DNS race.
        val networkMonitor = NetworkMonitor(applicationContext)
        val rdioClient = RdioClient(networkMonitor = networkMonitor)
        repository = RdioRepository(settings, rdioClient)
        audio = CallPlayer(applicationContext)
        click = ClickSound()
        downloader = Downloader(applicationContext, repository)

        // Feed the auto-jump toggle, threshold, and hold state into the player
        // at app scope so it works whether or not the UI / AudioService is up.
        settings.autoJump
            .onEach { audio.setAutoJump(it) }
            .launchIn(appScope)
        settings.autoJumpThreshold
            .onEach { audio.setAutoJumpThresholdMin(it) }
            .launchIn(appScope)
        repository.held
            .onEach { audio.setHoldActive(it != HoldState.None) }
            .launchIn(appScope)
    }

    override fun onTerminate() {
        super.onTerminate()
        audio.release()
        click.release()
        downloader.shutdown()
        repository.shutdown()
    }
}
