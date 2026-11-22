#!/usr/bin/env python3
"""Sign and verify a MyGo Android APK with the fixed Android release key."""
import argparse
import base64
import hashlib
import os
from pathlib import Path
import re
import secrets
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parents[1]


def run(args, **kwargs):
    return subprocess.run(args, check=True, text=True, **kwargs)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("input", type=Path)
    parser.add_argument("output", type=Path)
    parser.add_argument("version")
    parser.add_argument("--test-signing", action="store_true")
    args = parser.parse_args()
    sdk = os.environ.get("ANDROID_HOME") or os.environ.get("ANDROID_SDK_ROOT")
    if not sdk:
        raise ValueError("ANDROID_HOME is required")
    tools = Path(sdk) / "build-tools" / "35.0.1"
    apksigner, aapt, zipalign = tools / "apksigner", tools / "aapt", tools / "zipalign"
    version = args.version.removeprefix("v")
    if not re.fullmatch(r"\d+\.\d+\.\d+(?:-(?:alpha|beta|rc)\.\d+)?", version):
        raise ValueError("expected a semantic release version")
    env = os.environ.copy()
    with tempfile.TemporaryDirectory(prefix="easyagent-mygo-sign-") as tmp:
        key = Path(tmp) / "signing.p12"
        if args.test_signing:
            env["ANDROID_KEYSTORE_PASSWORD"] = secrets.token_urlsafe(32)
            run(["keytool", "-genkeypair", "-alias", "easyagent", "-keyalg", "RSA", "-keysize", "3072", "-validity", "2", "-dname", "CN=Disposable EasyAgent Signing", "-storetype", "PKCS12", "-keystore", str(key), "-storepass:env", "ANDROID_KEYSTORE_PASSWORD"], env=env, capture_output=True)
            cert = subprocess.check_output(["keytool", "-exportcert", "-alias", "easyagent", "-keystore", str(key), "-storepass:env", "ANDROID_KEYSTORE_PASSWORD"], env=env)
            expected = hashlib.sha256(cert).hexdigest()
        else:
            encoded = env.pop("ANDROID_KEYSTORE", "")
            if not encoded or not env.get("ANDROID_KEYSTORE_PASSWORD"):
                raise ValueError("Android release keystore and password are required")
            key.write_bytes(base64.b64decode(encoded, validate=True))
            expected = (ROOT / "mobile-native/signing-cert.sha256").read_text().strip().lower()
            if not re.fullmatch(r"[0-9a-f]{64}", expected):
                raise ValueError("invalid pinned signing certificate")
        key.chmod(0o600)
        args.output.parent.mkdir(parents=True, exist_ok=True)
        # V4 creates an extra .idsig sidecar for incremental installation. The
        # release distributes a standalone APK and its manifest rejects extras.
        run([str(apksigner), "sign", "--v4-signing-enabled", "false", "--ks", str(key), "--ks-key-alias", "easyagent", "--ks-pass", "env:ANDROID_KEYSTORE_PASSWORD", "--out", str(args.output), str(args.input)], env=env)
        signature = subprocess.check_output([str(apksigner), "verify", "--verbose", "--print-certs", str(args.output)], text=True)
        found = re.findall(r"Signer #\d+ certificate SHA-256 digest: ([0-9a-fA-F]{64})", signature)
        if [x.lower() for x in found] != [expected]:
            raise ValueError("MyGo APK certificate does not match the expected identity")
        badging = subprocess.check_output([str(aapt), "dump", "badging", str(args.output)], text=True)
        package = re.search(r"^package: (.+)$", badging, re.M)
        fields = dict(re.findall(r"(\w+)='([^']*)'", package.group(1))) if package else {}
        parts = version.split("-", 1)[0].split(".")
        code = int(parts[0]) * 10000 + int(parts[1]) * 100 + int(parts[2])
        if fields.get("name") != "com.hwj.easyagent" or fields.get("versionName") != version or fields.get("versionCode") != str(code):
            raise ValueError("MyGo APK package or version does not match release")
        if "application-debuggable" in badging:
            raise ValueError("MyGo APK must not be debuggable")
        if "native-code: 'arm64-v8a'" not in badging:
            raise ValueError("MyGo APK must contain the arm64 runtime")
        run([str(zipalign), "-c", "-p", "4", str(args.output)])
        print(f"Verified MyGo Android {version}, certificate SHA-256 {expected}")


if __name__ == "__main__":
    main()
