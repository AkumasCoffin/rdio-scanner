package solutions.saubeo.rdioscanner.data.protocol

import kotlinx.serialization.KSerializer
import kotlinx.serialization.Serializable
import kotlinx.serialization.descriptors.SerialDescriptor
import kotlinx.serialization.descriptors.buildClassSerialDescriptor
import kotlinx.serialization.encoding.Decoder
import kotlinx.serialization.encoding.Encoder
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonDecoder
import kotlinx.serialization.json.JsonEncoder
import kotlinx.serialization.json.JsonNull
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.int
import kotlinx.serialization.json.jsonArray
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive

@Serializable
data class ServerVersion(
    val version: String,
    val branding: String? = null,
    val email: String? = null,
)

@Serializable
data class TalkgroupDto(
    val id: Int,
    val label: String,
    val name: String = "",
    val group: String = "",
    val tag: String = "",
    val frequency: Double? = null,
    val led: String? = null,
    /** Name of an alert preset (alert1..alert9) to play before this talkgroup's calls. */
    val alert: String? = null,
)

@Serializable
data class UnitDto(
    val id: Int,
    val label: String,
)

@Serializable
data class SystemDto(
    val id: Int,
    val label: String,
    val led: String? = null,
    val order: Int? = null,
    val talkgroups: List<TalkgroupDto> = emptyList(),
    val units: List<UnitDto> = emptyList(),
    /** System-level alert preset name; per-talkgroup alert wins. */
    val alert: String? = null,
)

@Serializable
data class OscillatorBeep(
    val begin: Float,
    val end: Float,
    val frequency: Int,
    /** Wire-side field is `type` ("square", "sine", "triangle", "sawtooth"). */
    val type: String = "square",
)

@Serializable
data class ConfigDto(
    val afs: String? = null,
    val alerts: Map<String, List<OscillatorBeep>>? = null,
    val branding: String? = null,
    val email: String? = null,
    val systems: List<SystemDto> = emptyList(),
    /** `{ groupName: { systemId: [talkgroupIds] } }` — mirrors webapp config.groups. */
    val groups: Map<String, Map<String, List<Int>>> = emptyMap(),
    /** `{ tagName: { systemId: [talkgroupIds] } }` — mirrors webapp config.tags. */
    val tags: Map<String, Map<String, List<Int>>> = emptyMap(),
    val tagsToggle: Boolean = false,
    val playbackGoesLive: Boolean = false,
    val showListenersCount: Boolean = false,
    val time12hFormat: Boolean = false,
    val umamiUrl: String? = null,
    val umamiWebsiteId: String? = null,
)

/**
 * Server encodes audio as `{type: "Buffer", data: [0, 23, 47, ...]}`.
 * This serializer flattens that into a ByteArray.
 */
object BufferAsByteArraySerializer : KSerializer<ByteArray> {
    override val descriptor: SerialDescriptor =
        buildClassSerialDescriptor("BufferObject")

    override fun deserialize(decoder: Decoder): ByteArray {
        val input = decoder as? JsonDecoder
            ?: error("BufferAsByteArraySerializer requires kotlinx.serialization JSON")
        val element = input.decodeJsonElement()
        if (element is JsonNull) return ByteArray(0)
        val array = element.jsonObject["data"]?.jsonArray ?: return ByteArray(0)
        val out = ByteArray(array.size)
        for (i in array.indices) {
            out[i] = (array[i].jsonPrimitive.int and 0xFF).toByte()
        }
        return out
    }

    override fun serialize(encoder: Encoder, value: ByteArray) {
        val out = encoder as? JsonEncoder
            ?: error("BufferAsByteArraySerializer requires kotlinx.serialization JSON")
        val data = buildJsonObject {
            put("type", JsonPrimitive("Buffer"))
            put("data", JsonArray(value.map { JsonPrimitive(it.toInt() and 0xFF) }))
        }
        out.encodeJsonElement(data)
    }
}

@Serializable
data class CallDto(
    val id: Long,
    @Serializable(with = BufferAsByteArraySerializer::class)
    val audio: ByteArray = ByteArray(0),
    val audioName: String? = null,
    val audioType: String? = null,
    val dateTime: String,
    val frequency: Double? = null,
    val patches: List<Int> = emptyList(),
    val source: Long? = null,
    val system: Int,
    val talkgroup: Int,
    /**
     * Whisper transcript for this call, if the server has one. Arrives
     * inline on the CAL payload when transcription completed before the
     * frame was sent. Late-arriving transcripts come through the TRX
     * push pathway (Phase 2) and are merged into the screen state via
     * [ScannerViewModel.transcripts].
     */
    val transcript: String? = null,
) {
    override fun equals(other: Any?): Boolean {
        if (this === other) return true
        if (other !is CallDto) return false
        return id == other.id && system == other.system && talkgroup == other.talkgroup
    }

    override fun hashCode(): Int = id.hashCode()
}

@Serializable
data class SearchOptions(
    val limit: Int = 200,
    val offset: Int = 0,
    // NB: Wire.json has encodeDefaults=false. If `sort`'s default matches
    // the value we want to send (-1 = newest first), kotlinx silently
    // omits the field, the server type-switch falls through to the
    // ascOrder branch, and we get oldest-first. Use 0 here as a sentinel
    // so the screen's explicit -1 always survives serialization.
    val sort: Int = 0,
    val system: Int? = null,
    val talkgroup: Int? = null,
    val group: String? = null,
    val tag: String? = null,
    val date: String? = null,
)

@Serializable
data class SearchResultCall(
    val id: Long,
    val dateTime: String,
    val system: Int,
    val talkgroup: Int,
    val audioName: String? = null,
    val audioType: String? = null,
    val frequency: Double? = null,
    /** Inline transcript text, set when the server has one for this call. */
    val transcript: String? = null,
    /**
     * Hint from the server that this call has a transcript on disk even
     * if `transcript` is null in this payload (e.g. row-list endpoints
     * that omit the text to save bandwidth). Lets the UI render a
     * "loading…" placeholder vs. a "no transcript" state distinctly.
     */
    val hasTranscript: Boolean = false,
)

@Serializable
data class SearchResults(
    val count: Int = 0,
    val dateStart: String? = null,
    val dateStop: String? = null,
    val options: SearchOptions = SearchOptions(),
    val results: List<SearchResultCall> = emptyList(),
)

/** `{sysId: {tgId: bool}}` selection payload for LFM. */
typealias LivefeedMap = Map<Int, Map<Int, Boolean>>

