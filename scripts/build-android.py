#!/usr/bin/env python3
"""Build a signed Android release and verify its identity before staging it."""
import argparse
import base64
import hashlib
import json
import os
from pathlib import Path
import re
import secrets
import shutil
import subprocess
import sys
import tempfile

from release import android_version, validate

ROOT = Path(__file__).resolve().parents[1]
BUILD_TOOLS = "35.0.1"


def build_tools():
    sdk = os.environ.get("ANDROID_HOME") or os.environ.get("ANDROID_SDK_ROOT")
    if not sdk:
        raise ValueError("ANDROID_HOME must point to the Android SDK")
    tools = Path(sdk) / "build-tools" / BUILD_TOOLS
    if not (tools / "apksigner").is_file() or not (tools / "aapt").is_file():
        raise ValueError(f"install Android SDK build-tools;{BUILD_TOOLS}")
    return tools


def signing_certificate():
    value = (ROOT / "mobile-native/signing-cert.sha256").read_text().strip().lower()
    if not re.fullmatch(r"[0-9a-f]{64}", value):
        raise ValueError("invalid pinned Android signing certificate")
    return value


def verify_apk(apk, tag, certificate, tools):
    name, code = android_version(tag)
    signing = subprocess.check_output([str(tools / "apksigner"), "verify", "--verbose", "--print-certs", str(apk)], text=True)
    digests = re.findall(r"Signer #\d+ certificate SHA-256 digest: ([0-9a-fA-F]{64})", signing)
    if [digest.lower() for digest in digests] != [certificate.lower()]:
        raise ValueError("APK signing certificate does not match the expected identity")
    badging = subprocess.check_output([str(tools / "aapt"), "dump", "badging", str(apk)], text=True)
    package = re.search(r"^package: (.+)$", badging, re.M)
    fields = dict(re.findall(r"(\w+)='([^']*)'", package.group(1))) if package else {}
    expected = {"name": "com.hwj.easyagent.mobile", "versionCode": str(code), "versionName": name}
    if any(fields.get(key) != value for key, value in expected.items()) or "sdkVersion:'26'" not in badging:
        raise ValueError("APK application ID, version or minimum SDK does not match the release")
    if re.search(r"^application-debuggable", badging, re.M):
        raise ValueError("release APK must not be debuggable")


def build(tag, directory, test_signing=False):
    if Path.cwd().resolve() != ROOT:
        raise ValueError("run the Android release builder from the repository root")
    name, code = android_version(tag)
    configured = json.loads((ROOT / "desktop-native/mygo.json").read_text())["version"]
    if configured != name:
        raise ValueError("Android version does not match the shared desktop-native/mygo.json version")
    if not test_signing:
        validate(tag)
    tools = build_tools()
    directory = Path(directory).resolve()
    if directory.exists():
        raise ValueError("output directory already exists; use a fresh staging directory")
    env = os.environ.copy()
    encoded = env.pop("ANDROID_KEYSTORE", "")
    if not test_signing and (not encoded or not env.get("ANDROID_KEYSTORE_PASSWORD")):
        raise ValueError("Android releases require ANDROID_KEYSTORE and ANDROID_KEYSTORE_PASSWORD")
    keytool = str(Path(env["JAVA_HOME"]) / "bin/keytool") if env.get("JAVA_HOME") else "keytool"
    with tempfile.TemporaryDirectory(prefix="easyagent-android-signing.") as temporary:
        keystore = Path(temporary) / "release.p12"
        env["ANDROID_KEYSTORE_FILE"] = str(keystore)
        if test_signing:
            # PRs exercise the real release build without receiving the release key.
            env["ANDROID_KEYSTORE_PASSWORD"] = secrets.token_urlsafe(32)
            subprocess.run([keytool, "-genkeypair", "-alias", "easyagent", "-keyalg", "RSA", "-keysize", "3072",
                            "-validity", "2", "-dname", "CN=Disposable EasyAgent Android Signing", "-storetype", "PKCS12",
                            "-keystore", str(keystore), "-storepass:env", "ANDROID_KEYSTORE_PASSWORD"],
                           env=env, check=True, capture_output=True)
            cert = subprocess.check_output([keytool, "-exportcert", "-alias", "easyagent", "-keystore", str(keystore),
                                            "-storepass:env", "ANDROID_KEYSTORE_PASSWORD"], env=env)
            certificate = hashlib.sha256(cert).hexdigest()
        else:
            keystore.write_bytes(base64.b64decode(encoded, validate=True))
            certificate = signing_certificate()
        keystore.chmod(0o600)
        subprocess.run([str(ROOT / "mobile-native/gradlew"), "--no-daemon", ":app:assembleRelease", ":app:lintRelease",
                        f"-PeasyagentVersion={name}", f"-PeasyagentVersionCode={code}"],
                       cwd=ROOT / "mobile-native", env=env, check=True)
        apk = ROOT / "mobile-native/app/build/outputs/apk/release/app-release.apk"
        verify_apk(apk, tag, certificate, tools)
        directory.mkdir(parents=True)
        shutil.copyfile(apk, directory / "easyagent-android.apk")
    print(f"Verified Android {name} ({code}), certificate SHA-256 {certificate}")
    print(f"Android artifact: {directory / 'easyagent-android.apk'}")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    builder = commands.add_parser("build")
    builder.add_argument("directory")
    builder.add_argument("--version", help="release tag; defaults to the configured application version")
    builder.add_argument("--test-signing", action="store_true", help="PR-only build with a disposable key; skips tag validation")
    verifier = commands.add_parser("verify")
    verifier.add_argument("version")
    verifier.add_argument("apk")
    args = parser.parse_args()
    try:
        if args.command == "build":
            tag = args.version or "v" + json.loads((ROOT / "desktop-native/mygo.json").read_text())["version"]
            build(tag, args.directory, args.test_signing)
        else:
            verify_apk(Path(args.apk), args.version, signing_certificate(), build_tools())
            print("Android release signature and metadata verified")
    except (ValueError, OSError, subprocess.CalledProcessError) as error:
        print(f"Android release rejected: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
