import importlib.util
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest import mock
import zipfile

import release


class AndroidPolicyTest(unittest.TestCase):
    def test_android_version_codes_follow_semver_order(self):
        tags = ['v0.19.0-alpha.1', 'v0.19.0-alpha.99', 'v0.19.0-beta.1',
                'v0.19.0-rc.1', 'v0.19.0-rc.99', 'v0.19.0', 'v0.19.1', 'v0.19.2', 'v0.20.0', 'v1.0.0']
        versions = [release.android_version(tag) for tag in tags]
        codes = [code for _, code in versions]
        self.assertEqual(codes, sorted(set(codes)))
        self.assertEqual(versions[5], ('0.19.0', 1900300))
        release.android_version('v209.99.99')
        for tag in ['v210.0.0', 'v0.100.0', 'v0.19.100', 'v0.19.0-rc.100', '0.19.0']:
            with self.subTest(tag=tag), self.assertRaises(ValueError):
                release.android_version(tag)

    def test_apk_is_mandatory_only_from_android_release_onward(self):
        for tag in ['v0.18.0', 'v0.18.1']:
            self.assertEqual(release.android_assets(tag), ())
            self.assertEqual(len(release.required_assets(tag)) + 2, 13)
        for tag in ['v0.19.0-alpha.1', 'v0.19.0', 'v0.19.1']:
            self.assertEqual(release.android_assets(tag), ('easyagent-android.apk',))
            self.assertEqual(len(release.required_assets(tag)) + 2, 14)
        for tag in ['v0.19.2-alpha.1', 'v0.19.2']:
            self.assertEqual(release.android_assets(tag), ('easyagent-android.apk', 'easyagent-android.zip'))
            self.assertEqual(len(release.required_assets(tag)) + 2, 15)
        # Linux desktop/server packaging joined the release manifest at v0.21.0.
        for tag in ['v0.21.0-alpha.1', 'v1.0.0']:
            self.assertEqual(release.android_assets(tag), ('easyagent-android.apk', 'easyagent-android.zip'))
            self.assertEqual(len(release.required_assets(tag)) + 2, 25)


class AndroidArchiveTest(unittest.TestCase):
    def test_only_identical_apk_is_accepted(self):
        with tempfile.TemporaryDirectory() as folder:
            apk, archive = Path(folder) / 'release.apk', Path(folder) / 'release.zip'
            apk.write_bytes(b'signed APK fixture')
            cases = [({'easyagent-android.apk': apk.read_bytes()}, True),
                     ({'easyagent-android.apk': b'changedAPK fixture'}, False),
                     ({'easyagent-android.apk': apk.read_bytes(), 'extra.txt': b'extra'}, False),
                     ({'../easyagent-android.apk': apk.read_bytes()}, False)]
            for contents, valid in cases:
                with self.subTest(contents=list(contents)):
                    with zipfile.ZipFile(archive, 'w') as bundle:
                        for name, data in contents.items(): bundle.writestr(name, data)
                    if valid:
                        release.verify_android_archive(apk, archive)
                    else:
                        with self.assertRaises(ValueError): release.verify_android_archive(apk, archive)
            archive.write_bytes(b'not a ZIP')
            with self.assertRaisesRegex(ValueError, 'invalid Android ZIP'):
                release.verify_android_archive(apk, archive)


class AndroidAPKTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        spec = importlib.util.spec_from_file_location('build_android', Path(__file__).with_name('build-android.py'))
        cls.builder = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(cls.builder)

    def verify(self, badging=None, certificate=None):
        certificate = certificate or 'a' * 64
        signing = f'Number of signers: 1\nSigner #1 certificate SHA-256 digest: {certificate}\n'
        badging = badging or "package: name='com.hwj.easyagent.mobile' versionCode='1900300' versionName='0.19.0'\nsdkVersion:'26'\n"
        with mock.patch.object(self.builder.subprocess, 'check_output', side_effect=[signing, badging]):
            self.builder.verify_apk(Path('release.apk'), 'v0.19.0', 'a' * 64, Path('/sdk/build-tools/35.0.1'))

    def test_signed_release_metadata(self):
        self.verify()

    def test_wrong_signer_or_debuggable_apk_is_rejected(self):
        with self.assertRaisesRegex(ValueError, 'certificate'):
            self.verify(certificate='b' * 64)
        with self.assertRaisesRegex(ValueError, 'debuggable'):
            self.verify(badging="package: name='com.hwj.easyagent.mobile' versionCode='1900300' versionName='0.19.0'\nsdkVersion:'26'\napplication-debuggable\n")

    def test_wrong_app_version_or_minimum_sdk_is_rejected(self):
        correct = "package: name='com.hwj.easyagent.mobile' versionCode='1900300' versionName='0.19.0'\nsdkVersion:'26'\n"
        for old, new in [('com.hwj.easyagent.mobile', 'another.app'), ('1900300', '1'), ('0.19.0', '0.1.0'), ("sdkVersion:'26'", "sdkVersion:'21'")]:
            with self.subTest(value=new), self.assertRaises(ValueError):
                self.verify(badging=correct.replace(old, new))

    def test_unsigned_or_corrupted_apk_is_rejected(self):
        with mock.patch.object(self.builder.subprocess, 'check_output', side_effect=subprocess.CalledProcessError(1, 'apksigner')):
            with self.assertRaises(subprocess.CalledProcessError):
                self.builder.verify_apk(Path('release.apk'), 'v0.19.0', 'a' * 64, Path('/sdk/build-tools/35.0.1'))


if __name__ == '__main__':
    unittest.main()
