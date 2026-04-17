# kotlinx.serialization
-keepattributes *Annotation*, InnerClasses
-dontnote kotlinx.serialization.AnnotationsKt

-keep,includedescriptorclasses class solutions.saubeo.rdioscanner.**$$serializer { *; }
-keepclassmembers class solutions.saubeo.rdioscanner.** {
    *** Companion;
}
-keepclasseswithmembers class solutions.saubeo.rdioscanner.** {
    kotlinx.serialization.KSerializer serializer(...);
}

-keep class kotlinx.serialization.** { *; }

# OkHttp
-dontwarn okhttp3.**
-dontwarn okio.**

# Media3
-keep class androidx.media3.** { *; }
-dontwarn androidx.media3.**
