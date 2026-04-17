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
)

@Serializable
data class ConfigDto(
    val afs: String? = null,
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
    val sort: Int = -1,
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

