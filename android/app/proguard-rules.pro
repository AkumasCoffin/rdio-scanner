# ──────────────────────────────────────────────────────────────────────────
# kotlinx.serialization
# Official consumer rules from the kotlinx-serialization docs, adapted to
# this app's package. These are required because R8 otherwise strips or
# renames generated $serializer classes, companion object serializer()
# methods, object singletons referenced via @Serializable(with = …), and
# annotation metadata that the runtime reads reflectively.
# ──────────────────────────────────────────────────────────────────────────
-keepattributes *Annotation*, InnerClasses, RuntimeVisibleAnnotations, AnnotationDefault
-dontnote kotlinx.serialization.AnnotationsKt

# Generated $serializer classes for @Serializable classes.
-keep,includedescriptorclasses class solutions.saubeo.rdioscanner.**$$serializer { *; }

# Companion objects of @Serializable classes (needed so we can call
# MyClass.Companion.serializer()).
-if @kotlinx.serialization.Serializable class solutions.saubeo.rdioscanner.**
-keepclassmembers class <1> {
    static <1>$Companion Companion;
}

# serializer() method on any Companion (default or named).
-if @kotlinx.serialization.Serializable class solutions.saubeo.rdioscanner.** {
    static **$* *;
}
-keepclassmembers class <2>$<3> {
    kotlinx.serialization.KSerializer serializer(...);
}

# @Serializable object singletons: keep INSTANCE + serializer().
-if @kotlinx.serialization.Serializable class solutions.saubeo.rdioscanner.** {
    public static ** INSTANCE;
}
-keepclassmembers class <1> {
    public static <1> INSTANCE;
    kotlinx.serialization.KSerializer serializer(...);
}

# Custom KSerializer singletons referenced via @Serializable(with = …):
# the class + its serialize/deserialize methods must survive R8 because
# the reference is through an annotation, which R8 can't statically trace.
-keep class solutions.saubeo.rdioscanner.data.protocol.BufferAsByteArraySerializer {
    public static ** INSTANCE;
    *;
}

# Keep kotlinx.serialization runtime classes used reflectively.
-keep class kotlinx.serialization.** { *; }
-dontwarn kotlinx.serialization.**

# ──────────────────────────────────────────────────────────────────────────
# OkHttp / Okio
# ──────────────────────────────────────────────────────────────────────────
-dontwarn okhttp3.**
-dontwarn okio.**
-dontwarn org.conscrypt.**
-dontwarn org.bouncycastle.**
-dontwarn org.openjsse.**

# ──────────────────────────────────────────────────────────────────────────
# Media3 / ExoPlayer
# ──────────────────────────────────────────────────────────────────────────
-keep class androidx.media3.** { *; }
-dontwarn androidx.media3.**

# Protect the Android Service discovered reflectively by the system.
-keep class solutions.saubeo.rdioscanner.audio.AudioService { *; }

# ──────────────────────────────────────────────────────────────────────────
# Kotlin coroutines — debug-metadata helpers can be stripped but stack traces
# get less useful. Keep until a user-visible size problem appears.
# ──────────────────────────────────────────────────────────────────────────
-keepclassmembers class kotlinx.coroutines.** {
    volatile <fields>;
}
-dontwarn kotlinx.coroutines.**
