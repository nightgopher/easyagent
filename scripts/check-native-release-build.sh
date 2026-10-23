#!/usr/bin/env bash
# Exercise the native release/tag contract in an isolated checkout; never publish.
set -euo pipefail
root=$(git rev-parse --show-toplevel)
python3 "$root/scripts/check-native-keychain.py"
fixture=$(python3 -c 'import tempfile; print(tempfile.mkdtemp(prefix="easyagent-native-release.", suffix=".noindex"))')
trap 'rm -rf "$fixture"' EXIT
git clone --quiet --no-local "$root" "$fixture/repo"
cd "$fixture/repo"
git config user.name 'Release fixture'
git config user.email 'release-test@example.invalid'
version=v999.0.0-rc.1
(cd desktop-native && go run github.com/egoist/mygo/cmd/mygo keygen -o "$fixture/keys")
export MYGO_UPDATER_PRIVATE_KEY=$(cat "$fixture/keys/mygo-update.key")
export EASYAGENT_TEST_PUBLIC_KEY=$(cat "$fixture/keys/mygo-update.pub")
export MACOS_CERTIFICATE_PASSWORD='disposable-native-signing-fixture'
python3 - "$version" "$fixture" <<'PY'
import base64, hashlib, json, pathlib, sys, os, subprocess, ssl
version = sys.argv[1]
folder = pathlib.Path(sys.argv[2])
certificate, key, archive = [folder / name for name in ('codesign.crt', 'codesign.key', 'codesign.p12')]
openssl = folder / 'openssl.cnf'
openssl.write_text('[req]\ndistinguished_name=dn\nx509_extensions=code\nprompt=no\n[dn]\nCN=Disposable EasyAgent Native Signing\n[code]\nbasicConstraints=critical,CA:FALSE\nkeyUsage=critical,digitalSignature\nextendedKeyUsage=codeSigning\n')
subprocess.run(['openssl','req','-new','-x509','-newkey','rsa:2048','-nodes','-days','1','-config',str(openssl),'-keyout',str(key),'-out',str(certificate)],check=True,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
subprocess.run(['openssl','pkcs12','-export','-inkey',str(key),'-in',str(certificate),'-out',str(archive),'-passout','env:MACOS_CERTIFICATE_PASSWORD','-keypbe','PBE-SHA1-3DES','-certpbe','PBE-SHA1-3DES','-macalg','sha1'],check=True,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
fingerprint = hashlib.sha1(ssl.PEM_cert_to_DER_cert(certificate.read_text())).hexdigest().upper()
p = pathlib.Path('desktop-native/mygo.json')
config = json.loads(p.read_text())
config['version'] = version[1:]
config['updates']['publicKey'] = os.environ['EASYAGENT_TEST_PUBLIC_KEY']
config['macos']['signingIdentity'] = fingerprint
p.write_text(json.dumps(config, indent=2) + '\n')
p = pathlib.Path('CHANGELOG.md')
p.write_text(p.read_text() + f'\n## [{version}]\n\nDisposable native release verification.\n')
PY
export MACOS_CERTIFICATE=$(base64 -i "$fixture/codesign.p12")
git add -- CHANGELOG.md desktop-native/mygo.json
git -c commit.gpgsign=false commit --quiet -m 'test: local native release fixture'
git update-ref refs/remotes/origin/main HEAD
git -c tag.gpgsign=false tag -a "$version" -m 'Local test only; never publish'
bash scripts/build-native.sh darwin-arm64 dist "$version"
test -s dist/easyagent-native-darwin-arm64.dmg
"desktop-native/build.noindex/darwin-arm64/EasyAgent.app/Contents/Resources/easyagent" --version | grep -Fx "easyagent $version"

# A new version must satisfy the old designated requirements, as TCC does.
python3 - "$fixture/requirements.json" <<'PY'
import json, pathlib, re, subprocess, sys
app = pathlib.Path('desktop-native/build.noindex/darwin-arm64/EasyAgent.app')
paths = [app, app / 'Contents/Resources/cua-helper/EasyAgent Computer Use.app']
paths.append(app / 'Contents/Resources/easyagent-credentials')
records = []
for path in paths:
    result = subprocess.run(['codesign', '--display', '-r-', '-vvv', str(path)], check=True, capture_output=True, text=True)
    records.append({'path': str(path), 'stable': path.name == 'easyagent-credentials', 'requirement': result.stdout.split('designated =>', 1)[1].strip(), 'hash': re.search(r'^CDHash=(.+)$', result.stderr, re.M).group(1)})
pathlib.Path(sys.argv[1]).write_text(json.dumps(records))
PY
version=v999.0.0-rc.2
python3 - "$version" <<'PY'
import json, pathlib, sys
version = sys.argv[1]
p = pathlib.Path('desktop-native/mygo.json')
config = json.loads(p.read_text())
config['version'] = version[1:]
p.write_text(json.dumps(config, indent=2) + '\n')
p = pathlib.Path('CHANGELOG.md')
p.write_text(p.read_text() + f'\n## [{version}]\n\nDisposable identity-preservation verification.\n')
PY
git add -- CHANGELOG.md desktop-native/mygo.json
git -c commit.gpgsign=false commit --quiet -m 'test: second native signing fixture version'
git update-ref refs/remotes/origin/main HEAD
git -c tag.gpgsign=false tag -a "$version" -m 'Local test only; never publish'
bash scripts/build-native.sh darwin-arm64 dist-next "$version"
python3 - "$fixture/requirements.json" <<'PY'
import json, pathlib, re, subprocess, sys
for previous in json.loads(pathlib.Path(sys.argv[1]).read_text()):
    path = previous['path']
    subprocess.run(['codesign', '--verify', '--strict', '-R', '=' + previous['requirement'], path], check=True)
    result = subprocess.run(['codesign', '--display', '-r-', '-vvv', path], check=True, capture_output=True, text=True)
    current = re.search(r'^CDHash=(.+)$', result.stderr, re.M).group(1)
    if previous['stable']:
        assert current == previous['hash'], 'ordinary app updates changed the credential helper hash'
    else:
        assert current != previous['hash'], 'fixture code identity did not change'
print('Changed bundles preserve their designated requirements and reuse the identical credential helper')
PY
