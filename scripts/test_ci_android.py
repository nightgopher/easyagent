import os
from pathlib import Path
import re
import subprocess
import tempfile
import textwrap
import unittest


ROOT = Path(__file__).resolve().parents[1]


class AndroidPreparationTests(unittest.TestCase):
    def run_hook(self, available_kb, build_exit="0"):
        with tempfile.TemporaryDirectory() as directory:
            fixture = Path(directory)
            commands = fixture / "bin"
            commands.mkdir()
            (fixture / "android-build.exit").write_text(build_exit)
            (fixture / "android-build.log").write_text("fixture compilation log\n")
            recorded = fixture / "cleanup-arguments"
            stubs = {
                "timeout": '#!/bin/sh\nshift\nexec "$@"\n',
                "df": f"#!/bin/sh\nprintf 'Filesystem 1024-blocks Used Available Capacity Mounted\\nfixture 20000000 1000 {available_kb} 1%% /\\n'\n",
                "sudo": '#!/bin/sh\nprintf "%s\\n" "$@" > "$CLEANUP_ARGUMENTS"\n',
            }
            for name, content in stubs.items():
                path = commands / name
                path.write_text(content)
                path.chmod(0o700)
            env = {
                **os.environ,
                "PATH": str(commands) + os.pathsep + os.environ["PATH"],
                "RUNNER_TEMP": str(fixture),
                "RUNNER_TOOL_CACHE": str(fixture / "toolcache"),
                "CLEANUP_ARGUMENTS": str(recorded),
            }
            workflow = (ROOT / ".github/workflows/android-ui.yml").read_text()
            hook = re.search(r"          pre-emulator-launch-script: \|\n(.*?)          script: \|", workflow, re.S)
            self.assertIsNotNone(hook)
            # Match the action's separate `sh -c` invocation for each line:
            # multiline if statements and variables must stay inside a script.
            outputs = []
            for line in textwrap.dedent(hook.group(1)).strip().splitlines():
                result = subprocess.run(
                    ["sh", "-c", line], cwd=ROOT / "mobile-native", env=env,
                    text=True, capture_output=True, timeout=5,
                )
                outputs.append(result.stdout + result.stderr)
                if result.returncode:
                    break
            arguments = recorded.read_text().splitlines() if recorded.exists() else []
            return result.returncode, "".join(outputs), arguments, env["RUNNER_TOOL_CACHE"]

    def test_spacious_runner_skips_cleanup(self):
        code, output, arguments, _ = self.run_hook(20 * 1024 * 1024)
        self.assertEqual(code, 0, output)
        self.assertIn("fixture compilation log", output)
        self.assertEqual(arguments, [])

    def test_tight_runner_removes_only_unused_toolchains(self):
        code, output, arguments, toolcache = self.run_hook(6 * 1024 * 1024)
        self.assertEqual(code, 0, output)
        self.assertEqual(arguments, ["rm", "-rf", "/usr/share/dotnet", toolcache + "/CodeQL"])

    def test_failed_build_stops_before_cleanup(self):
        code, output, arguments, _ = self.run_hook(6 * 1024 * 1024, "2")
        self.assertNotEqual(code, 0, output)
        self.assertIn("fixture compilation log", output)
        self.assertEqual(arguments, [])


if __name__ == "__main__":
    unittest.main()
