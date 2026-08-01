package solutions.saubeo.rdioscanner.data.protocol

/** One (system, talkgroup) pair inside a selector section. */
data class GroupedTalkgroup(
    val system: SystemDto,
    val talkgroup: TalkgroupDto,
)

/** A labelled section of the talkgroup selector — one group, or one tag. */
data class GroupSection(
    val label: String,
    val talkgroups: List<GroupedTalkgroup>,
)

/**
 * Resolves one of the server's label maps ([ConfigDto.groups] or
 * [ConfigDto.tags], both shaped `{ label: { systemId: [talkgroupId] } }`) into
 * renderable selector sections.
 *
 * Ordering and skip rules mirror the webapp's rebuildGroupSections /
 * rebuildTagSections so both clients lay out the same config identically:
 * labels sorted, systems ascending by id within a label, talkgroups left in
 * the order the server sent them, ids that don't resolve dropped, and empty
 * sections omitted rather than rendered as blank cards. Ids can fail to
 * resolve legitimately — a restricted access code scopes the systems list per
 * client while the label maps still name everything.
 *
 * Note the map's system keys are JSON object keys, so they arrive as strings.
 *
 * Kept free of Compose types so it stays a plain function over plain data.
 */
fun buildSections(
    systems: List<SystemDto>,
    labelMap: Map<String, Map<String, List<Int>>>,
): List<GroupSection> {
    if (systems.isEmpty() || labelMap.isEmpty()) return emptyList()

    val systemsById = systems.associateBy { it.id }
    val talkgroupsBySystem = systems.associate { it.id to it.talkgroups.associateBy { tg -> tg.id } }

    return labelMap.keys.sorted().mapNotNull { label ->
        val systemMap = labelMap[label].orEmpty()

        // Carry the original key through rather than re-deriving it from the
        // parsed id — the lookup must not assume the server spelled the id
        // canonically.
        val talkgroups = systemMap.keys
            .mapNotNull { key -> key.toIntOrNull()?.let { it to key } }
            .sortedBy { (systemId, _) -> systemId }
            .flatMap { (systemId, key) ->
                val system = systemsById[systemId] ?: return@flatMap emptyList<GroupedTalkgroup>()
                val byId = talkgroupsBySystem[systemId].orEmpty()

                systemMap[key].orEmpty().mapNotNull { talkgroupId ->
                    byId[talkgroupId]?.let { GroupedTalkgroup(system, it) }
                }
            }

        if (talkgroups.isEmpty()) null else GroupSection(label, talkgroups)
    }
}
