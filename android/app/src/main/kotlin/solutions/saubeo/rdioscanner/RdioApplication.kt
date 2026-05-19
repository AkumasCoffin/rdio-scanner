package solutions.saubeo.rdioscanner

import android.app.Application
import solutions.saubeo.rdioscanner.audio.CallPlayer
import solutions.saubeo.rdioscanner.audio.ClickSound
import solutions.saubeo.rdioscanner.data.client.NetworkMonitor
import solutions.saubeo.rdioscanner.data.client.RdioClient
import solutions.saubeo.rdioscanner.data.prefs.SettingsStore
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
    }

    override fun onTerminate() {
        super.onTerminate()
        audio.release()
        click.release()
        downloader.shutdown()
        repository.shutdown()
    }
}
