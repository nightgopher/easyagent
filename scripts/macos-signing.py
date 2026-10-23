#!/usr/bin/env python3
"""Build with the pinned macOS identity. Private keys never enter release assets."""
from contextlib import contextmanager
import base64
import json
import os
from pathlib import Path
import re
import signal
import stat
import subprocess
import sys
import tempfile

ROOT = Path(__file__).resolve().parent.parent


def run(args, **kwargs):
    try:
        return subprocess.check_output(args, stderr=subprocess.DEVNULL, **kwargs)
    except (subprocess.CalledProcessError, OSError):
        # CalledProcessError includes argv, which may contain a keychain password.
        raise RuntimeError("macOS signing operation failed") from None


def identity(config):
    value = config.get("macos", {}).get("signingIdentity", "")
    if not re.fullmatch(r"[A-Fa-f0-9]{40}", value):
        raise ValueError("A pinned certificate fingerprint is required; ad hoc releases are refused")
    return value.upper()


def private_file(path):
    path = Path(path)
    info, parent = path.lstat(), path.parent.lstat()
    if (not stat.S_ISREG(info.st_mode) or info.st_uid != os.getuid() or info.st_mode & 0o077
            or not stat.S_ISDIR(parent.st_mode) or parent.st_uid != os.getuid() or parent.st_mode & 0o077):
        raise ValueError("Signing files must be private to the current user")
    return path


def keychains():
    return [json.loads(line.strip()) for line in run(["security", "list-keychains", "-d", "user"], text=True).splitlines() if line.strip()]


@contextmanager
def search_list(keychain, recovery=None):
    keychain = str(keychain)
    previous = keychains()
    if recovery is not None:
        recovery.write_text(json.dumps(previous))
        recovery.chmod(0o600)
    try:
        if keychain not in previous:
            run(["security", "list-keychains", "-d", "user", "-s", *previous, keychain])
        yield
    finally:
        if keychain not in previous:
            current = keychains()
            run(["security", "list-keychains", "-d", "user", "-s", *[p for p in current if p != keychain]])
        if recovery is not None:
            recovery.unlink()


@contextmanager
def local_signing_lock(folder, keychain):
    lock = folder / '.codesign-lock'
    try:
        lock.mkdir(mode=0o700)
    except FileExistsError:
        pid = int(private_file(lock / 'pid').read_text())
        if pid <= 1:
            raise ValueError('Invalid signing lock')
        try:
            os.kill(pid, 0)
        except ProcessLookupError:
            recovery = lock / 'original.json'
            if recovery.exists():
                previous = json.loads(private_file(recovery).read_text())
                if not isinstance(previous, list) or not all(isinstance(p, str) for p in previous):
                    raise ValueError('Invalid signing recovery snapshot')
                if str(keychain) not in previous:
                    run(['security', 'list-keychains', '-d', 'user', '-s', *[p for p in keychains() if p != str(keychain)]])
                recovery.unlink()
            (lock / 'pid').unlink()
        else:
            raise ValueError('EasyAgent signing is already in progress')
    (lock / 'pid').write_text(str(os.getpid()))
    (lock / 'pid').chmod(0o600)
    try:
        yield lock / 'original.json'
    finally:
        # Keep a failed restoration journal so the next process can recover it.
        if not (lock / 'original.json').exists():
            (lock / 'pid').unlink()
            lock.rmdir()


@contextmanager
def signing_keychain(expected):
    encoded = os.environ.get("MACOS_CERTIFICATE", "")
    if encoded:
        password = os.environ.get("MACOS_CERTIFICATE_PASSWORD")
        if password is None:
            raise ValueError("MACOS_CERTIFICATE_PASSWORD is required")
        with tempfile.TemporaryDirectory(prefix="easyagent-native-signing.", suffix=".noindex") as directory:
            folder = Path(directory)
            certificate = folder / "identity.p12"
            certificate.write_bytes(base64.b64decode(encoded, validate=True))
            certificate.chmod(0o600)
            keychain = folder / "signing.keychain-db"
            secret = os.urandom(24).hex()
            run(["security", "create-keychain", "-p", secret, str(keychain)])
            try:
                run(["security", "set-keychain-settings", "-lut", "21600", str(keychain)])
                run(["security", "unlock-keychain", "-p", secret, str(keychain)])
                run(["security", "import", str(certificate), "-k", str(keychain), "-P", password, "-T", "/usr/bin/codesign"])
                run(["security", "set-key-partition-list", "-S", "apple-tool:,apple:", "-s", "-k", secret, str(keychain)])
                with search_list(keychain):
                    verify_identity(keychain, expected)
                    yield
            finally:
                run(["security", "delete-keychain", str(keychain)])
    else:
        filename = Path(os.environ.get("EA_MAC_SIGNING_CONFIG", Path.home() / "Library/Application Support/EasyAgent Signing/identity.json"))
        if not filename.is_file():
            raise ValueError("Fixed signing certificate is missing; refusing an ad hoc release")
        config = json.loads(private_file(filename).read_text())
        if config.get("version") != 1 or str(config.get("identity", "")).upper() != expected:
            raise ValueError("Local signing identity does not match the pinned release certificate")
        if not all(isinstance(config.get(k), str) and Path(config[k]).is_absolute() for k in ['keychain', 'passwordFile']):
            raise ValueError('Signing keychain and password paths must be absolute')
        keychain = private_file(config["keychain"])
        password = private_file(config["passwordFile"]).read_text().strip()
        # Use the existing certificate; do not import it or change its trust settings.
        with local_signing_lock(filename.parent, keychain) as recovery:
            run(["security", "unlock-keychain", "-p", password, str(keychain)])
            with search_list(keychain, recovery):
                verify_identity(keychain, expected)
                yield


def verify_identity(keychain, expected):
    found = run(["security", "find-identity", "-p", "codesigning", str(keychain)], text=True)
    if not re.search(r"\b" + re.escape(expected) + r"\b", found, re.I):
        raise ValueError("The keychain does not contain the pinned signing identity")


def verify_bundle(bundle, expected):
    for path, identifier in [(bundle, "com.hwj.easyagent"),
                             (bundle / "Contents/Resources/easyagent-credentials", "easyagent-credentials"),
                             (bundle / "Contents/Resources/cua-helper/EasyAgent Computer Use.app", "com.hwj.easyagent.cua-helper")]:
        requirement = f'=identifier "{identifier}" and certificate leaf = H"{expected}"'
        run(["codesign", "--verify", "--deep", "--strict", "-R", requirement, str(path)])
        displayed = run(["codesign", "--display", "-r-", str(path)], text=True)
        if "cdhash" in displayed.lower():
            raise ValueError("The bundle has a version-specific designated requirement")


def main():
    if sys.platform != "darwin":
        raise ValueError("macOS release signing requires macOS")
    config = json.loads((ROOT / "desktop-native/mygo.json").read_text())
    expected = identity(config)
    command = sys.argv[1:]
    if command[:1] == ["--"]:
        command = command[1:]
    if not command:
        raise ValueError("Expected a native build command")
    child_env = os.environ.copy()
    child_env.pop("MACOS_CERTIFICATE", None)
    child_env.pop("MACOS_CERTIFICATE_PASSWORD", None)
    bundle = ROOT / "desktop-native/build.noindex/darwin-arm64/EasyAgent.app"
    try:
        with signing_keychain(expected):
            subprocess.run([*command, "-sign", expected], env=child_env, check=True)
            verify_bundle(bundle, expected)
    finally:
        subprocess.run([sys.executable, str(ROOT / "scripts/unregister-desktop-apps.py"), str(bundle)], check=True)


if __name__ == "__main__":
    signal.signal(signal.SIGTERM, lambda *_: sys.exit(1))
    try:
        main()
    except (ValueError, RuntimeError, OSError, subprocess.CalledProcessError):
        print("Native signing failed; no ad hoc fallback. Check the pinned certificate and private signing configuration.", file=sys.stderr)
        sys.exit(1)
