import importlib.util
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('macos_signing', Path(__file__).with_name('macos-signing.py'))
signing = importlib.util.module_from_spec(spec)
spec.loader.exec_module(signing)


class NativeSigningTests(unittest.TestCase):
    def test_missing_or_ad_hoc_identity_is_refused(self):
        for identity in ['', '-', 'untrusted name', 'a' * 39]:
            with self.subTest(identity=identity), self.assertRaises(ValueError):
                signing.identity({'macos': {'signingIdentity': identity}})
        self.assertEqual(signing.identity({'macos': {'signingIdentity': 'a' * 40}}), 'A' * 40)

    def test_private_configuration_rejects_shared_files_and_symlinks(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'config.json'
            path.write_text('{}')
            path.chmod(0o644)
            with self.assertRaises(ValueError):
                signing.private_file(path)
            path.chmod(0o600)
            self.assertEqual(signing.private_file(path), path)
            link = path.with_name('link')
            link.symlink_to(path)
            with self.assertRaises(ValueError):
                signing.private_file(link)

    def test_search_list_restores_own_change_after_failed_build(self):
        state = ['login', 'user-keychain']

        def command(args, **_):
            nonlocal state
            if '-s' in args:
                state = args[args.index('-s') + 1:]
                return b''
            return '\n'.join(json.dumps(item) for item in state)

        with patch.object(signing, 'run', side_effect=command):
            with self.assertRaises(RuntimeError):
                with signing.search_list('temporary-signing'):
                    state.append('concurrent-user-keychain')
                    raise RuntimeError('build failed')
        self.assertEqual(state, ['login', 'user-keychain', 'concurrent-user-keychain'])

    def test_existing_search_list_entry_is_retained(self):
        with patch.object(signing, 'keychains', return_value=['signing']), patch.object(signing, 'run') as run:
            with signing.search_list('signing'):
                pass
            run.assert_not_called()

    def test_different_certificate_is_rejected(self):
        with patch.object(signing, 'run', return_value='1) ' + 'B' * 40):
            with self.assertRaises(ValueError):
                signing.verify_identity('keychain', 'A' * 40)

    def test_command_failure_does_not_expose_password(self):
        secret = 'private-keychain-password'
        with patch.object(subprocess, 'check_output', side_effect=subprocess.CalledProcessError(1, ['security', '-p', secret])):
            with self.assertRaises(RuntimeError) as caught:
                signing.run(['security', '-p', secret])
        self.assertNotIn(secret, str(caught.exception))

    def test_version_specific_requirement_is_rejected(self):
        with patch.object(signing, 'run', side_effect=[b'', 'designated => cdhash H"1234"']):
            with self.assertRaises(ValueError):
                signing.verify_bundle(Path('EasyAgent.app'), 'A' * 40)

    def test_live_signing_lock_is_not_removed(self):
        with tempfile.TemporaryDirectory() as directory:
            folder = Path(directory)
            lock = folder / '.codesign-lock'
            lock.mkdir(mode=0o700)
            pid = lock / 'pid'
            pid.write_text(str(os.getpid()))
            pid.chmod(0o600)
            with self.assertRaises(ValueError):
                with signing.local_signing_lock(folder, 'signing'):
                    self.fail('concurrent signer acquired the lock')
            self.assertTrue(pid.exists())

    def test_interrupted_signing_search_list_is_recovered(self):
        with tempfile.TemporaryDirectory() as directory:
            folder = Path(directory)
            lock = folder / '.codesign-lock'
            lock.mkdir(mode=0o700)
            for name, value in [('pid', '12345'), ('original.json', '["login"]')]:
                path = lock / name
                path.write_text(value)
                path.chmod(0o600)
            with patch.object(os, 'kill', side_effect=ProcessLookupError), patch.object(signing, 'keychains', return_value=['login', 'signing', 'other']), patch.object(signing, 'run') as run:
                with signing.local_signing_lock(folder, 'signing'):
                    pass
                self.assertEqual(run.call_args.args[0], ['security', 'list-keychains', '-d', 'user', '-s', 'login', 'other'])
            self.assertFalse(lock.exists())


if __name__ == '__main__':
    unittest.main()
