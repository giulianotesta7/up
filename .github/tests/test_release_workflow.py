"""Exercise release-note validation and publication without contacting GitHub."""
import os
from pathlib import Path
import subprocess
import tempfile
import unittest


WORKFLOW = Path(__file__).resolve().parents[1] / "workflows" / "release.yml"


def run_block(workflow, name):
    step = workflow.split(f"      - name: {name}\n", 1)[1].split("\n      - ", 1)[0]
    return "\n".join(
        line[10:] for line in step.split("        run: |\n", 1)[1].splitlines()
    )


class ReleaseNotesTest(unittest.TestCase):
    def test_release_notes(self):
        workflow = WORKFLOW.read_text()
        self.assertIn("name: Validate release notes", workflow)
        self.assertLess(workflow.index("name: Validate release notes"), workflow.index("name: Test"))
        validate = run_block(workflow, "Validate release notes")
        publish = run_block(workflow, "Publish release")
        self.assertNotIn("--generate-notes", publish)
        for contents in (None, "", " \n\t\n", "\u00a0\n", "## Fixes\n\nA verified fix.\n"):
            with self.subTest(contents=contents), tempfile.TemporaryDirectory() as scratch:
                root = Path(scratch)
                notes = root / ".github/release-notes/v9.9.9.md"
                notes.parent.mkdir(parents=True)
                if contents is not None:
                    notes.write_text(contents)
                stub = root / "gh"
                stub.write_text('#!/bin/bash\nprintf "%s\\n" "$@" > "$GH_LOG"\n')
                stub.chmod(0o755)
                log = root / "gh.log"
                env = dict(os.environ, RELEASE_TAG="v9.9.9", GH_LOG=str(log), PATH=f"{root}:{os.environ['PATH']}")
                result = subprocess.run(
                    ["bash", "--noprofile", "--norc", "-e", "-o", "pipefail", "-c", validate + "\n" + publish],
                    cwd=root, env=env, capture_output=True, text=True,
                )
                self.assertEqual(result.stdout, "")
                if contents is None or not contents.strip():
                    self.assertEqual(result.returncode, 1)
                    self.assertEqual(result.stderr, "::error::Release notes must exist and contain non-whitespace text: .github/release-notes/v9.9.9.md\n")
                    self.assertFalse(log.exists(), "Rejected notes must never invoke gh")
                else:
                    self.assertEqual(result.returncode, 0, result.stderr)
                    self.assertEqual(result.stderr, "")
                    self.assertEqual(log.read_text().splitlines(), [
                        "release", "create", "v9.9.9",
                        "dist/up_linux_amd64.tar.gz", "dist/up_linux_arm64.tar.gz",
                        "dist/checksums.txt", "dist/up.service", "dist/up-system.service",
                        "--title", "v9.9.9", "--notes-file",
                        ".github/release-notes/v9.9.9.md", "--verify-tag",
                    ])
                self.assertEqual(notes.read_text() if notes.exists() else None, contents)


if __name__ == "__main__":
    unittest.main()
