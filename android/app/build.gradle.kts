import java.util.Properties

plugins {
    alias(libs.plugins.android.application)
    alias(libs.plugins.kotlin.android)
    alias(libs.plugins.kotlin.compose)
    alias(libs.plugins.kotlin.serialization)
}

// Load release keystore credentials from signing.properties if present,
// otherwise from RDIO_KEYSTORE_* environment variables. When neither is
// available, release builds fall back to the debug keystore so local
// assembleRelease still works — suitable for CI sideload artifacts but
// NOT for a Play Store upload.
val releaseSigningProps: Properties? = rootProject.file("signing.properties")
    .takeIf { it.exists() }
    ?.let { file -> Properties().apply { file.inputStream().use(::load) } }

fun signingSetting(key: String): String? =
    releaseSigningProps?.getProperty(key)?.takeIf { it.isNotBlank() }
        ?: System.getenv("RDIO_${key.uppercase()}")?.takeIf { it.isNotBlank() }

val hasReleaseKeystore = signingSetting("storeFile") != null

android {
    namespace = "solutions.saubeo.rdioscanner"
    compileSdk = 35

    defaultConfig {
        applicationId = "solutions.saubeo.rdioscanner"
        minSdk = 26
        targetSdk = 35
        versionCode = 1
        versionName = "1.0.0"
        vectorDrawables.useSupportLibrary = true
    }

    signingConfigs {
        if (hasReleaseKeystore) {
            create("release") {
                storeFile = rootProject.file(signingSetting("storeFile")!!)
                storePassword = signingSetting("storePassword")
                keyAlias = signingSetting("keyAlias")
                keyPassword = signingSetting("keyPassword")
            }
        }
    }

    buildTypes {
        debug {
            applicationIdSuffix = ".debug"
            isDebuggable = true
        }
        release {
            isMinifyEnabled = true
            isShrinkResources = true
            proguardFiles(
                getDefaultProguardFile("proguard-android-optimize.txt"),
                "proguard-rules.pro"
            )
            signingConfig = if (hasReleaseKeystore) {
                signingConfigs.getByName("release")
            } else {
                logger.warn(
                    "⚠️  No release keystore found — assembleRelease will sign " +
                        "with the debug key. Configure signing.properties or the " +
                        "RDIO_STOREFILE/STOREPASSWORD/KEYALIAS/KEYPASSWORD env " +
                        "vars before publishing."
                )
                signingConfigs.getByName("debug")
            }
        }
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }

    kotlinOptions {
        jvmTarget = "17"
        freeCompilerArgs += listOf(
            "-opt-in=kotlinx.serialization.ExperimentalSerializationApi",
        )
    }

    buildFeatures {
        compose = true
        buildConfig = true
    }

    packaging {
        resources {
            excludes += "/META-INF/{AL2.0,LGPL2.1}"
        }
    }

    // Rename APK outputs to rdio-<versionName>.apk (release) or
    // rdio-<versionName>-debug.apk so release assets live at predictable
    // paths. defaultConfig.versionName drives the filename.
    applicationVariants.all {
        val variant = this
        outputs.all {
            (this as? com.android.build.gradle.internal.api.BaseVariantOutputImpl)
                ?.outputFileName = buildString {
                    append("rdio-")
                    append(variant.versionName)
                    if (variant.buildType.name != "release") {
                        append("-")
                        append(variant.buildType.name)
                    }
                    append(".apk")
                }
        }
    }
}

dependencies {
    implementation(libs.androidx.core.ktx)
    implementation(libs.androidx.lifecycle.runtime.ktx)
    implementation(libs.androidx.lifecycle.runtime.compose)
    implementation(libs.androidx.lifecycle.viewmodel.compose)
    implementation(libs.androidx.lifecycle.service)
    implementation(libs.androidx.activity.compose)

    implementation(platform(libs.androidx.compose.bom))
    implementation(libs.androidx.compose.ui)
    implementation(libs.androidx.compose.ui.graphics)
    implementation(libs.androidx.compose.ui.tooling.preview)
    implementation(libs.androidx.compose.material3)
    implementation(libs.androidx.compose.material.icons.extended)
    implementation(libs.androidx.navigation.compose)

    implementation(libs.androidx.datastore.preferences)

    implementation(libs.androidx.media3.exoplayer)
    implementation(libs.androidx.media3.session)
    implementation(libs.androidx.media3.datasource)

    implementation(libs.okhttp)
    implementation(libs.okhttp.logging)
    implementation(libs.kotlinx.serialization.json)
    implementation(libs.kotlinx.coroutines.android)
    implementation(libs.google.material)

    debugImplementation(libs.androidx.compose.ui.tooling)
}
