package solutions.saubeo.rdioscanner.ui

import android.app.Application
import androidx.lifecycle.AndroidViewModel
import androidx.lifecycle.viewModelScope
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.SharedFlow
import kotlinx.coroutines.flow.SharingStarted
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.flow.stateIn
import kotlinx.coroutines.launch
import solutions.saubeo.rdioscanner.RdioApplication
import solutions.saubeo.rdioscanner.data.client.ConnectionState
import solutions.saubeo.rdioscanner.data.prefs.ConnectionProfileDto
import solutions.saubeo.rdioscanner.data.prefs.PresetDto
import solutions.saubeo.rdioscanner.data.protocol.ConfigDto
import solutions.saubeo.rdioscanner.data.protocol.SearchOptions
import solutions.saubeo.rdioscanner.data.protocol.SearchResults
import solutions.saubeo.rdioscanner.data.repository.DownloadEvent
import solutions.saubeo.rdioscanner.data.repository.HoldState

class ScannerViewModel(app: Application) : AndroidViewModel(app) {
    private val rdioApp = app as RdioApplication
    private val repo get() = rdioApp.repository
    private val player get() = rdioApp.audio
    private val downloader get() = rdioApp.downloader

    val state: StateFlow<ConnectionState> = repo.state
    val config: StateFlow<ConfigDto?> = repo.config
    val listeners = repo.listeners
    val livefeedActive = repo.livefeedActive
    val livefeedEnabled: StateFlow<Boolean> = repo.livefeedEnabled
    val held: StateFlow<HoldState> = repo.held
    val paused: StateFlow<Boolean> = repo.paused
    val avoided: StateFlow<Set<Pair<Int, Int>>> = repo.avoided

    val queue = player.queue
    val playing = player.playing
    val isPlaying = player.isPlaying
    val history = player.history

    val searchResults: StateFlow<SearchResults?> = repo.searchResults
    val searching: StateFlow<Boolean> = repo.searching
    val downloads: SharedFlow<DownloadEvent> = downloader.events

    val selection: StateFlow<Map<Int, Map<Int, Boolean>>> =
        rdioApp.settings.selection.stateIn(
            viewModelScope, SharingStarted.Eagerly, emptyMap()
        )

    val presets: StateFlow<List<PresetDto>> =
        rdioApp.settings.presets.stateIn(
            viewModelScope, SharingStarted.Eagerly, emptyList()
        )

    val profiles: StateFlow<List<ConnectionProfileDto>> =
        rdioApp.settings.profiles.stateIn(
            viewModelScope, SharingStarted.Eagerly, emptyList()
        )

    val lastProfileId: StateFlow<String?> =
        rdioApp.settings.lastProfileId.stateIn(
            viewModelScope, SharingStarted.Eagerly, null
        )

    private val _serverUrl = MutableStateFlow("")
    val serverUrl: StateFlow<String> = _serverUrl.asStateFlow()
    private val _accessCode = MutableStateFlow("")
    val accessCode: StateFlow<String> = _accessCode.asStateFlow()

    init {
        viewModelScope.launch {
            _serverUrl.value = rdioApp.settings.serverUrl.first()
            _accessCode.value = rdioApp.settings.accessCode.first()
        }
    }

    fun onServerUrl(value: String) { _serverUrl.value = value }
    fun onAccessCode(value: String) { _accessCode.value = value }

    fun connect() {
        val url = _serverUrl.value
        val code = _accessCode.value
        viewModelScope.launch { repo.connect(url, code) }
    }

    fun tryReconnect() {
        viewModelScope.launch { repo.connectWithSavedCredentials() }
    }

    fun disconnect() {
        repo.disconnect()
        player.stopAndClear()
    }

    fun connectProfile(profile: ConnectionProfileDto) {
        viewModelScope.launch { repo.connectProfile(profile) }
    }

    fun saveProfile(
        name: String,
        url: String,
        accessCode: String,
        editing: ConnectionProfileDto? = null,
    ) {
        if (name.isBlank() || url.isBlank()) return
        val profile = ConnectionProfileDto(
            id = editing?.id ?: repo.newProfileId(),
            name = name.trim(),
            serverUrl = url.trim(),
            accessCode = accessCode.trim(),
            createdAt = editing?.createdAt ?: System.currentTimeMillis(),
        )
        viewModelScope.launch { repo.saveProfile(profile) }
    }

    fun deleteProfile(id: String) {
        viewModelScope.launch { repo.deleteProfile(id) }
    }

    fun toggleTalkgroup(system: Int, talkgroup: Int, active: Boolean) {
        viewModelScope.launch {
            repo.updateSelection { current ->
                val next = current.toMutableMap()
                val inner = next[system]?.toMutableMap() ?: mutableMapOf()
                inner[talkgroup] = active
                next[system] = inner
                next
            }
        }
    }

    fun toggleSystem(system: Int, talkgroupIds: List<Int>, active: Boolean) {
        viewModelScope.launch {
            repo.updateSelection { current ->
                current.toMutableMap().also {
                    it[system] = talkgroupIds.associateWith { active }
                }
            }
        }
    }

    fun setAll(active: Boolean) {
        val cfg = config.value ?: return
        viewModelScope.launch {
            repo.setSelection(
                cfg.systems.associate { sys ->
                    sys.id to sys.talkgroups.associate { tg -> tg.id to active }
                }
            )
        }
    }

    // ----- scanner buttons -----

    fun toggleLivefeed() {
        val next = !livefeedEnabled.value
        repo.setLivefeedEnabled(next)
        if (!next) {
            // Webapp parity: stopping the livefeed clears the queue and halts audio.
            player.stopAndClear()
            repo.setPaused(false)
        }
    }

    fun holdTalkgroup() {
        if (held.value is HoldState.Talkgroup) {
            repo.releaseHold()
            return
        }
        val cur = playing.value ?: history.value.firstOrNull() ?: return
        repo.holdTalkgroup(cur.call.system, cur.call.talkgroup)
    }

    fun holdSystem() {
        if (held.value is HoldState.System) {
            repo.releaseHold()
            return
        }
        val cur = playing.value ?: history.value.firstOrNull() ?: return
        repo.holdSystem(cur.call.system)
    }

    fun releaseHold() = repo.releaseHold()

    fun togglePause() {
        val next = !paused.value
        repo.setPaused(next)
        if (next) player.player.pause() else player.player.play()
    }

    fun skip() = player.skip()

    /** Webapp behavior: play `this.call || this.callPrevious`. */
    fun replayLast() {
        val target = playing.value ?: history.value.firstOrNull() ?: return
        player.replay(target)
    }

    /** AVOID the current (or most-recent) talkgroup and skip whatever's playing. */
    fun avoidCurrent() {
        val cur = playing.value ?: history.value.firstOrNull() ?: return
        repo.avoid(cur.call.system, cur.call.talkgroup)
        if (playing.value != null) player.skip()
    }

    fun unavoid(system: Int, talkgroup: Int) {
        repo.unavoid(system, talkgroup)
    }

    fun clearAvoids() = repo.clearAvoids()

    fun stopAudio() = player.stopAndClear()

    // ----- presets -----

    fun savePreset(name: String, selectedMap: Map<Int, Map<Int, Boolean>>, editing: PresetDto? = null) {
        val list = selectedMap.mapNotNull { (sys, tgs) ->
            val ids = tgs.filterValues { it }.keys.toList()
            if (ids.isEmpty()) null else sys to ids
        }.toMap()
        val preset = PresetDto(
            id = editing?.id ?: repo.newPresetId(),
            name = name.trim(),
            talkgroups = list,
            createdAt = editing?.createdAt ?: System.currentTimeMillis(),
        )
        viewModelScope.launch { repo.savePreset(preset) }
    }

    fun deletePreset(id: String) {
        viewModelScope.launch { repo.deletePreset(id) }
    }

    fun applyPreset(preset: PresetDto) {
        viewModelScope.launch { repo.applyPreset(preset) }
    }

    // ----- search -----

    fun runSearch(opts: SearchOptions) {
        repo.searchCalls(opts)
    }

    fun playSearchResult(id: Long) {
        repo.requestCall(id, flag = "p")
    }

    fun downloadSearchResult(id: Long) {
        repo.requestCall(id, flag = "d")
    }
}
