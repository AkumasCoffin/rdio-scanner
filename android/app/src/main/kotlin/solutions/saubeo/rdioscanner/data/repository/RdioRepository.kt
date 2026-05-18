package solutions.saubeo.rdioscanner.data.repository

import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.SharedFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.combine
import kotlinx.coroutines.flow.filter
import kotlinx.coroutines.flow.filterNotNull
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.flow.launchIn
import kotlinx.coroutines.flow.map
import kotlinx.coroutines.flow.onEach
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch
import solutions.saubeo.rdioscanner.data.client.ConnectionState
import solutions.saubeo.rdioscanner.data.client.RdioClient
import solutions.saubeo.rdioscanner.data.client.RdioCredentials
import solutions.saubeo.rdioscanner.data.prefs.ConnectionProfileDto
import solutions.saubeo.rdioscanner.data.prefs.PresetDto
import solutions.saubeo.rdioscanner.data.prefs.SettingsStore
import solutions.saubeo.rdioscanner.data.protocol.CallDto
import solutions.saubeo.rdioscanner.data.protocol.ConfigDto
import solutions.saubeo.rdioscanner.data.protocol.SearchOptions
import solutions.saubeo.rdioscanner.data.protocol.SearchResults
import solutions.saubeo.rdioscanner.data.protocol.ServerVersion
import java.util.UUID

/**
 * Glues the [RdioClient] to persisted settings and exposes a stable
 * observable surface to the UI and the audio service.
 */
class RdioRepository(
    internal val settings: SettingsStore,
    private val client: RdioClient = RdioClient(),
) {
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.Default)

    val state: StateFlow<ConnectionState> = client.state
    val version: StateFlow<ServerVersion?> = client.version
    val config: StateFlow<ConfigDto?> = client.config
    val listeners: StateFlow<Int> = client.listeners
    val livefeedActive: StateFlow<Boolean> = client.livefeedActive
    val searchResults: StateFlow<SearchResults?> = client.searchResults
    val searching: StateFlow<Boolean> = client.searching

    /** CALs whose flag is null (default) or "p"; the ones that should play. */
    val playbackCalls: Flow<Pair<CallDto, String?>> =
        client.calls.filter { (_, flag) -> flag != FLAG_DOWNLOAD }

    /** CALs whose flag is "d"; downstream should save these to disk. */
    val downloadedCalls: Flow<CallDto> =
        client.calls.filter { (_, flag) -> flag == FLAG_DOWNLOAD }.map { it.first }

    /**
     * Tick flow that emits Unit on every call ingested via the live feed.
     * SearchScreen subscribes (with debounce) so its result list refreshes
     * as new calls land — otherwise it'd freeze at whatever the last
     * explicit search returned until the user changed a filter.
     */
    val liveCallTick: Flow<Unit> = client.calls.map { Unit }

    private val _held = MutableStateFlow<HoldState>(HoldState.None)
    val held: StateFlow<HoldState> = _held.asStateFlow()

    private val _paused = MutableStateFlow(false)
    val paused: StateFlow<Boolean> = _paused.asStateFlow()

    private val _livefeedEnabled = MutableStateFlow(true)
    val livefeedEnabled: StateFlow<Boolean> = _livefeedEnabled.asStateFlow()

    private val _avoided = MutableStateFlow<Set<Pair<Int, Int>>>(emptySet())
    val avoided: StateFlow<Set<Pair<Int, Int>>> = _avoided.asStateFlow()

    /**
     * In-memory cache of (callId → transcript) pairs. Populated by both
     * the inline transcript on CAL frames and the async TRX flow. Survives
     * for the lifetime of the repository (i.e. the process) — webapp uses
     * the same volatile-only model, and Whisper text is cheap to re-fetch
     * if needed.
     */
    private val _transcripts = MutableStateFlow<Map<Long, String>>(emptyMap())
    val transcripts: StateFlow<Map<Long, String>> = _transcripts.asStateFlow()

    init {
        // Always send a COMPLETE livefeed matrix: every talkgroup in the
        // server's config, with any missing saved entries defaulting to true.
        // Relying on `settings.selection` alone was fragile — if the selection
        // map was partial (stale state, first-load races, config additions),
        // the map sent to the server would drop every missing TG and the
        // server's matrix would end up mostly false.
        combine(config.filterNotNull(), settings.selection) { cfg, sel ->
            buildFullLivefeedMap(cfg, sel)
        }.onEach { fullMap ->
            if (_livefeedEnabled.value && fullMap.isNotEmpty()) {
                client.setLivefeedMap(fullMap)
            }
        }.launchIn(scope)

        // On first config (no saved selection yet), persist all-on so the
        // SelectorScreen shows every TG as enabled.
        config.filterNotNull().onEach { cfg ->
            val initialized = settings.selectionInitialized.first()
            if (!initialized) {
                val everything = cfg.systems.associate { sys ->
                    sys.id to sys.talkgroups.associate { tg -> tg.id to true }
                }
                settings.setSelection(everything, markInitialized = true)
            }
        }.launchIn(scope)

        // Collect every TRX (request reply or push) into the transcript
        // cache. Empty strings are kept as-is — they signal "server has
        // no transcript" so the UI can stop retrying.
        client.transcripts.onEach { (id, text) ->
            _transcripts.update { current ->
                if (current[id] == text) current else current + (id to text)
            }
        }.launchIn(scope)

        // Whenever a CAL arrives carrying an inline transcript, merge it
        // into the same cache so screens have a single source of truth
        // regardless of which path the text came in on.
        client.calls.onEach { (call, _) ->
            val inline = call.transcript?.takeIf { it.isNotBlank() }
            if (inline != null) {
                _transcripts.update { current ->
                    if (current[call.id] == inline) current else current + (call.id to inline)
                }
            }
        }.launchIn(scope)
    }

    /**
     * Ask the server for the transcript of [callId]. Result arrives
     * asynchronously through [transcripts]. Re-requesting an id that's
     * already cached is harmless — the server replies, the value
     * goes through the same merge path, and the StateFlow only emits
     * if the text actually changed.
     */
    fun requestTranscript(callId: Long) {
        if (callId <= 0) return
        client.requestTranscript(callId)
    }

    /**
     * Wipe the in-memory transcript cache. Called on profile switch so
     * a row from profile-B with a coincidentally-matching call id can't
     * surface profile-A's transcript text.
     */
    fun clearTranscripts() {
        _transcripts.value = emptyMap()
    }

    /**
     * Drop any cached search payload held by the client. Used during the
     * profile-switch reset so the Search screen doesn't momentarily render
     * profile-A's results against profile-B's config.
     */
    fun clearSearchResults() = client.clearSearchResults()

    private fun buildFullLivefeedMap(
        cfg: ConfigDto,
        selection: Map<Int, Map<Int, Boolean>>,
    ): Map<Int, Map<Int, Boolean>> = cfg.systems.associate { sys ->
        val innerSel = selection[sys.id].orEmpty()
        sys.id to sys.talkgroups.associate { tg ->
            tg.id to (innerSel[tg.id] ?: true)
        }
    }

    suspend fun connectWithSavedCredentials(): Boolean {
        // Prefer the last-used profile; fall back to the legacy serverUrl/accessCode
        // keys so users upgrading from the pre-profile build keep working.
        val lastId = settings.lastProfileId.first()
        val profiles = settings.currentProfiles()
        val profile = lastId?.let { id -> profiles.firstOrNull { it.id == id } }
            ?: profiles.firstOrNull()
        if (profile != null) {
            connectProfile(profile)
            return true
        }
        val url = settings.serverUrl.first()
        if (url.isBlank()) return false
        val code = settings.accessCode.first()
        client.connect(RdioCredentials(url, code.ifBlank { null }))
        return true
    }

    suspend fun connect(url: String, accessCode: String) {
        settings.setServer(url.trim(), accessCode.trim())
        client.connect(RdioCredentials(url.trim(), accessCode.trim().ifBlank { null }))
    }

    suspend fun connectProfile(profile: ConnectionProfileDto) {
        settings.setActiveProfile(
            url = profile.serverUrl.trim(),
            accessCode = profile.accessCode.trim(),
            profileId = profile.id,
        )
        // Reset livefeed-on as part of the per-session state so a profile
        // switch can't inherit profile-A's "off" toggle — otherwise
        // AudioService would silently drop every incoming call on the new
        // profile while the LIVE FEED button flips active via the LFM ack.
        _livefeedEnabled.value = true
        client.connect(
            RdioCredentials(
                profile.serverUrl.trim(),
                profile.accessCode.trim().ifBlank { null },
            )
        )
    }

    suspend fun saveProfile(profile: ConnectionProfileDto) {
        val existing = settings.currentProfiles().toMutableList()
        val idx = existing.indexOfFirst { it.id == profile.id }
        if (idx >= 0) existing[idx] = profile else existing.add(profile)
        settings.saveProfiles(existing)
    }

    suspend fun deleteProfile(id: String) {
        val existing = settings.currentProfiles()
        settings.saveProfiles(existing.filterNot { it.id == id })
        if (settings.lastProfileId.first() == id) settings.setLastProfileId(null)
    }

    fun newProfileId(): String = UUID.randomUUID().toString()

    fun disconnect() {
        client.disconnect()
    }

    // All selection edits funnel through here so back-to-back toggles on the
    // ViewModel can't race each other and lose an intermediate write.
    private val selectionMutex = Mutex()

    suspend fun setSelection(map: Map<Int, Map<Int, Boolean>>) {
        selectionMutex.withLock { settings.setSelection(map) }
    }

    /**
     * Atomic read-modify-write of the current selection. Callers pass a
     * transform that receives the latest persisted state; the returned map
     * is written back. Avoids the stale-StateFlow races that happened when
     * the ViewModel read `selection.value` and persisted without locking.
     */
    suspend fun updateSelection(
        transform: (Map<Int, Map<Int, Boolean>>) -> Map<Int, Map<Int, Boolean>>,
    ) {
        selectionMutex.withLock {
            val current = settings.selection.first()
            settings.setSelection(transform(current))
        }
    }

    fun holdTalkgroup(system: Int, talkgroup: Int) {
        _held.value = HoldState.Talkgroup(system, talkgroup)
    }

    fun holdSystem(system: Int) {
        _held.value = HoldState.System(system)
    }

    fun releaseHold() {
        _held.value = HoldState.None
    }

    fun setPaused(value: Boolean) {
        _paused.value = value
    }

    /** Toggles the livefeed subscription without closing the WebSocket. */
    fun setLivefeedEnabled(enabled: Boolean) {
        _livefeedEnabled.value = enabled
        scope.launch {
            if (enabled) {
                val cfg = config.value
                val sel = settings.selection.first()
                if (cfg != null) {
                    val fullMap = buildFullLivefeedMap(cfg, sel)
                    if (fullMap.isNotEmpty()) client.setLivefeedMap(fullMap)
                }
            } else {
                // Webapp parity: send `[LFM]` with no payload so the server
                // clears its matrix and the connection stays open.
                client.clearLivefeed()
            }
        }
    }

    fun avoid(system: Int, talkgroup: Int) {
        _avoided.value = _avoided.value + (system to talkgroup)
    }

    fun unavoid(system: Int, talkgroup: Int) {
        _avoided.value = _avoided.value - (system to talkgroup)
    }

    fun clearAvoids() {
        _avoided.value = emptySet()
    }

    fun isAvoided(system: Int, talkgroup: Int): Boolean =
        (system to talkgroup) in _avoided.value

    fun requestCall(id: Long, flag: String? = null) {
        client.requestCall(id, flag)
    }

    fun searchCalls(opts: SearchOptions) {
        client.search(opts)
    }

    suspend fun savePreset(preset: PresetDto) {
        val existing = settings.currentPresets().toMutableList()
        val idx = existing.indexOfFirst { it.id == preset.id }
        if (idx >= 0) existing[idx] = preset else existing.add(preset)
        settings.savePresets(existing)
    }

    suspend fun deletePreset(id: String) {
        val existing = settings.currentPresets()
        settings.savePresets(existing.filterNot { it.id == id })
    }

    suspend fun applyPreset(preset: PresetDto) {
        val cfg = config.value
        val map: Map<Int, Map<Int, Boolean>> = cfg?.systems?.associate { sys ->
            val tgs = preset.talkgroups[sys.id]?.toSet().orEmpty()
            sys.id to sys.talkgroups.associate { tg -> tg.id to (tg.id in tgs) }
        } ?: preset.talkgroups.mapValues { (_, ids) -> ids.associateWith { true } }
        settings.setSelection(map)
    }

    fun newPresetId(): String = UUID.randomUUID().toString()

    fun shutdown() {
        client.shutdown()
    }

    companion object {
        const val FLAG_PLAY = "p"
        const val FLAG_DOWNLOAD = "d"
    }
}

sealed interface HoldState {
    data object None : HoldState
    data class System(val id: Int) : HoldState
    data class Talkgroup(val system: Int, val id: Int) : HoldState

    fun allows(call: CallDto): Boolean = when (this) {
        None -> true
        is System -> call.system == id
        is Talkgroup -> call.system == system && call.talkgroup == id
    }
}
