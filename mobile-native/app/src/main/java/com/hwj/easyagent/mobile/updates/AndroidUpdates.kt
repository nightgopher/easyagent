package com.hwj.easyagent.mobile.updates

import android.content.ClipData
import android.content.Context
import android.content.Intent
import android.content.pm.ApplicationInfo
import android.content.pm.PackageInfo
import android.content.pm.PackageManager
import android.os.Build
import androidx.core.content.FileProvider
import com.hwj.easyagent.mobile.BuildConfig
import java.io.File
import java.security.MessageDigest

internal fun createAppUpdater(context: Context): AppUpdater {
    val prefs = context.getSharedPreferences("easyagent-updates", Context.MODE_PRIVATE)
    val installed = installedPackage(context)
    val directory = File(context.filesDir, "updates")
    // A new process has no live download or installer owned by this updater.
    directory.listFiles()?.filter { it.extension == "part" || (it.extension == "apk" &&
        (releaseVersionCode(it.nameWithoutExtension) ?: 0) <= installed.versionCodeCompat()) }?.forEach { it.delete() }
    return AppUpdater(
        InstalledVersion(installed.versionName.orEmpty(), installed.versionCodeCompat(), BuildConfig.SOURCE_TREE), UpdateClient(), directory,
        object : UpdatePreferences {
            override var automatic: Boolean
                get() = prefs.getBoolean("automatic", true)
                set(value) { prefs.edit().putBoolean("automatic", value).apply() }
            override var lastAttempt: Long
                get() = prefs.getLong("last-attempt", 0)
                set(value) { prefs.edit().putLong("last-attempt", value).apply() }
            override var dismissedTag: String
                get() = prefs.getString("dismissed-tag", "").orEmpty()
                set(value) { prefs.edit().putString("dismissed-tag", value).apply() }
        },
        { file, update -> verifyAndroidApk(context, file, update) },
    )
}

@Suppress("DEPRECATION")
private fun signatureFlags() = if (Build.VERSION.SDK_INT >= 28) PackageManager.GET_SIGNING_CERTIFICATES else PackageManager.GET_SIGNATURES
@Suppress("DEPRECATION")
private fun installedPackage(context: Context): PackageInfo = context.packageManager.getPackageInfo(context.packageName, signatureFlags())
@Suppress("DEPRECATION")
private fun PackageInfo.versionCodeCompat(): Long = if (Build.VERSION.SDK_INT >= 28) longVersionCode else versionCode.toLong()
@Suppress("DEPRECATION")
private fun PackageInfo.signers(): Set<String> {
    val signatures = if (Build.VERSION.SDK_INT >= 28) signingInfo?.apkContentsSigners else signatures
    return signatures.orEmpty().map { MessageDigest.getInstance("SHA-256").digest(it.toByteArray()).hex() }.toSet()
}

@Suppress("DEPRECATION")
internal fun verifyAndroidApk(context: Context, file: File, update: AppUpdate) {
    val current = installedPackage(context)
    val candidate = context.packageManager.getPackageArchiveInfo(file.absolutePath, signatureFlags())
        ?: throw UpdateException("安装包无法识别，请重新下载")
    if ((candidate.applicationInfo?.minSdkVersion ?: Int.MAX_VALUE) > Build.VERSION.SDK_INT) {
        throw UpdateException("新版本不支持当前 Android 系统")
    }
    if (candidate.packageName != context.packageName || candidate.versionName != update.version ||
        candidate.versionCodeCompat() != update.code || update.code <= InstalledVersion(current.versionName.orEmpty(), current.versionCodeCompat()).comparisonCode) {
        throw UpdateException("安装包的应用或版本不匹配，已阻止安装")
    }
    val signers = current.signers()
    if (signers.isEmpty() || candidate.signers() != signers) throw UpdateException("安装包签名与当前应用不一致，无法覆盖更新")
    if (current.applicationInfo?.flags?.and(ApplicationInfo.FLAG_DEBUGGABLE) == 0 &&
        candidate.applicationInfo?.flags?.and(ApplicationInfo.FLAG_DEBUGGABLE) != 0) throw UpdateException("正式版本不能安装测试更新包")
}

internal fun updateInstallIntent(context: Context, file: File): Intent {
    val uri = FileProvider.getUriForFile(context, context.packageName + ".files", file)
    return Intent(Intent.ACTION_VIEW).apply {
        setDataAndType(uri, "application/vnd.android.package-archive")
        clipData = ClipData.newRawUri("EasyAgent 更新", uri)
        addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
    }
}
