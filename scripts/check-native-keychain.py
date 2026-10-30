#!/usr/bin/env python3
"""Exercise real macOS credential upgrades without touching user credentials."""
import base64
from concurrent.futures import ThreadPoolExecutor
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import re
import shutil
import ssl
import subprocess
import sys
import tempfile

ROOT = Path(__file__).resolve().parent.parent


def run(args, **kwargs):
    return subprocess.run(args, check=True, **kwargs)


def certificate(folder, name):
    config, key, cert, archive = [folder / (name + suffix) for suffix in ('.cnf', '.key', '.crt', '.p12')]
    config.write_text('[req]\ndistinguished_name=dn\nx509_extensions=code\nprompt=no\n'
                      '[dn]\nCN=Disposable EasyAgent Keychain Fixture\n'
                      '[code]\nbasicConstraints=critical,CA:FALSE\n'
                      'keyUsage=critical,digitalSignature\nextendedKeyUsage=codeSigning\n')
    run(['openssl', 'req', '-new', '-x509', '-newkey', 'rsa:2048', '-nodes', '-days', '1',
         '-config', str(config), '-keyout', str(key), '-out', str(cert)],
        stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    run(['openssl', 'pkcs12', '-export', '-inkey', str(key), '-in', str(cert), '-out', str(archive),
         '-passout', 'env:MACOS_CERTIFICATE_PASSWORD', '-keypbe', 'PBE-SHA1-3DES',  # ggignore: environment reference, not a password
         '-certpbe', 'PBE-SHA1-3DES', '-macalg', 'sha1'])
    key.chmod(0o600)
    archive.chmod(0o600)
    os.environ['MACOS_CERTIFICATE'] = base64.b64encode(archive.read_bytes()).decode()
    return hashlib.sha1(ssl.PEM_cert_to_DER_cert(cert.read_text())).hexdigest().upper()


def signature(path):
    result = run(['codesign', '--display', '-r-', '-vvv', str(path)], capture_output=True, text=True)
    return re.search(r'^CDHash=(.+)$', result.stderr, re.M).group(1)


def main():
    if sys.platform != 'darwin':
        raise RuntimeError('The credential upgrade fixture requires macOS')
    spec = importlib.util.spec_from_file_location('macos_signing', ROOT / 'scripts/macos-signing.py')
    signing = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(signing)
    with tempfile.TemporaryDirectory(prefix='easyagent-keychain-upgrade.', suffix='.noindex') as directory:
        folder = Path(directory)
        first, second, other_id, other_signer = [folder / name for name in ('first', 'second', 'other-id', 'other-signer')]
        helper, next_helper, third_helper = [folder / name for name in ('helper', 'next-helper', 'third-helper')]
        run(['go', 'build', '-trimpath', '-buildvcs=false', '-ldflags=-s -w', '-o', str(helper), './tools/credentials'], cwd=ROOT / 'desktop-native')
        run(['go', 'build', '-trimpath', '-buildvcs=false', '-ldflags=-s -w -buildid=next-helper-fixture',
             '-o', str(next_helper), './tools/credentials'], cwd=ROOT / 'desktop-native')
        run(['go', 'build', '-trimpath', '-buildvcs=false', '-ldflags=-s -w -buildid=third-helper-fixture',
             '-o', str(third_helper), './tools/credentials'], cwd=ROOT / 'desktop-native')
        run(['go', 'test', '-c', '-o', str(first), '.'], cwd=ROOT / 'desktop-native')
        run(['go', 'test', '-c', '-ldflags=-X=github.com/nightgopher/easyagent/desktop-native.nativeKeychainProbeBuild=second',
             '-o', str(second), '.'], cwd=ROOT / 'desktop-native')
        shutil.copy2(second, other_id)
        shutil.copy2(second, other_signer)
        os.environ['MACOS_CERTIFICATE_PASSWORD'] = os.urandom(24).hex()
        expected = certificate(folder, 'first-identity')

        def sign(path, fingerprint, identifier='com.hwj.easyagent'):
            requirement = f'identifier "{identifier}" and certificate leaf = H"{fingerprint}"'
            run(['codesign', '--force', '--sign', fingerprint, '--identifier', identifier,
                 '--options', 'runtime', '--timestamp=none', '-r', '=designated => ' + requirement, str(path)])
            run(['codesign', '--verify', '--strict', '-R', '=' + requirement, str(path)])

        with signing.signing_keychain(expected):
            sign(first, expected)
            sign(second, expected)
            sign(other_id, expected, 'com.hwj.easyagent.untrusted-fixture')
            sign(helper, expected, 'easyagent-credentials')
            sign(next_helper, expected, 'easyagent-credentials')
            sign(third_helper, expected, 'easyagent-credentials')
        first_hash = signature(first)
        if first_hash == signature(second):
            raise RuntimeError('The two upgrade builds must have different code hashes')
        if len({signature(helper), signature(next_helper), signature(third_helper)}) != 3:
            raise RuntimeError('The helper migration fixture must have different code hashes')
        other_helper = folder / "other-helper"
        shutil.copy2(helper, other_helper)
        second_identity = certificate(folder, 'second-identity')
        with signing.signing_keychain(second_identity):
            sign(other_signer, second_identity)
            sign(other_helper, second_identity, 'easyagent-credentials')
        rejected = run([str(helper)], input='{"Operation":"get","Account":"model"}', capture_output=True, text=True)
        response = json.loads(rejected.stdout)
        if not response.get('Error') or response.get('Value'):
            raise RuntimeError('The helper accepted an unauthenticated direct caller')

        # Keychains created outside ~/Library/Keychains use the legacy blob
        # format and skip partition checks, producing a false-positive upgrade
        # test. Use our own directory under the real keychain directory.
        keychain_folder = tempfile.TemporaryDirectory(prefix='easyagent-upgrade.', suffix='.noindex',
                                                      dir=Path.home() / 'Library/Keychains')
        keychain = Path(keychain_folder.name) / 'probe.keychain-db'
        try:
            run(['security', 'create-keychain', '-p', 'synthetic-keychain-fixture', str(keychain)])
            run(['security', 'unlock-keychain', '-p', 'synthetic-keychain-fixture', str(keychain)])
            steps = [(first, 'seed', helper), (first, 'migrate', helper), (second, 'upgrade', helper),
                     (other_id, 'reject', helper), (other_signer, 'reject', helper), (second, 'repair', helper),
                     (second, 'remove', helper),
                     (second, 'helper-upgrade', next_helper), (second, 'rotate', next_helper),
                     (second, 'latest', third_helper), (first, 'latest', helper), (second, 'clear', third_helper),
                     (first, 'relaunch', next_helper), (second, 'relaunch', helper),
                     (second, 'network', next_helper), (second, 'tamper', next_helper),
                     (second, 'locked', next_helper), (second, 'relaunch', next_helper)]
            for binary, action, current_helper in steps:
                if action == 'locked':
                    run(['security', 'lock-keychain', str(keychain)])
                elif action == 'relaunch':
                    run(['security', 'unlock-keychain', '-p', 'synthetic-keychain-fixture', str(keychain)])
                env = {**os.environ, 'EASYAGENT_KEYCHAIN_PROBE_PATH': str(keychain),
                       'EASYAGENT_KEYCHAIN_PROBE_CACHE': str(folder / 'app-data'),
                       'EASYAGENT_KEYCHAIN_PROBE_HELPER': str(current_helper),
                       'EASYAGENT_KEYCHAIN_PROBE_ACTION': action}
                # No signing material is needed by the child processes.
                env.pop('MACOS_CERTIFICATE', None)
                env.pop('MACOS_CERTIFICATE_PASSWORD', None)
                env.pop('MYGO_UPDATER_PRIVATE_KEY', None)
                run([str(binary), '-test.run=^TestNativeKeychainUpgradeProbe$', '-test.v', '-test.timeout=30s'],
                    env=env, timeout=40)
            def concurrent_write(item):
                binary, current_helper, account = item
                concurrent_env = {**env, 'EASYAGENT_KEYCHAIN_PROBE_ACTION': 'parallel',
                                  'EASYAGENT_KEYCHAIN_PROBE_HELPER': str(current_helper),
                                  'EASYAGENT_KEYCHAIN_PROBE_ACCOUNT': account}
                run([str(binary), '-test.run=^TestNativeKeychainUpgradeProbe$', '-test.v', '-test.timeout=30s'],
                    env=concurrent_env, timeout=40)
            with ThreadPoolExecutor(max_workers=2) as executor:
                list(executor.map(concurrent_write, [(first, helper, 'host/concurrent-a'),
                                                     (second, next_helper, 'host/concurrent-b')]))
            run([str(second), '-test.run=^TestNativeKeychainUpgradeProbe$', '-test.v', '-test.timeout=30s'],
                env={**env, 'EASYAGENT_KEYCHAIN_PROBE_ACTION': 'parallel-result'}, timeout=40)
            for binary, action, current_helper in [
                    (other_signer, 'certificate-change', other_helper),
                    (other_signer, 'certificate-change-relaunch', other_helper),
                    (first, 'certificate-change-old', helper)]:
                run([str(binary), '-test.run=^TestNativeKeychainUpgradeProbe$', '-test.v', '-test.timeout=30s'],
                    env={**env, 'EASYAGENT_KEYCHAIN_PROBE_ACTION': action,
                         'EASYAGENT_KEYCHAIN_PROBE_HELPER': str(current_helper)}, timeout=40)
        finally:
            if keychain.exists():
                run(['security', 'delete-keychain', str(keychain)])
            current = signing.keychains()
            remaining = [path for path in current if Path(path).resolve() != keychain.resolve()]
            if remaining != current:
                run(['security', 'list-keychains', '-d', 'user', '-s', *remaining])
            keychain_folder.cleanup()
        print('PASS: different signed builds retain host/model credentials; other identities are denied; '
              'legacy migration/repair and clearing survive relaunch without password UI')


if __name__ == '__main__':
    main()
