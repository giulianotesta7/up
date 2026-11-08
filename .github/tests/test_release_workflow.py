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
                tag = 'v1.2.3+build.1' if contents and contents.strip() else 'v9.9.9'
                notes = root / f".github/release-notes/{tag}.md"
                notes.parent.mkdir(parents=True)
                if contents is not None:
                    notes.write_text(contents)
                stub = root / "gh"
                stub.write_text('#!/bin/bash\nprintf "%s\\n" "$@" > "$GH_LOG"\n')
                stub.chmod(0o755)
                log = root / "gh.log"
                env = dict(os.environ, RELEASE_TAG=tag, GH_LOG=str(log), PATH=f"{root}:{os.environ['PATH']}")
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
                        "release", "create", tag,
                        "dist/up_linux_amd64.tar.gz", "dist/up_linux_arm64.tar.gz",
                        "dist/checksums.txt", "dist/up.service", "dist/up-system.service",
                        "--title", tag, "--notes-file",
                        f".github/release-notes/{tag}.md", "--verify-tag",
                    ])
                self.assertEqual(notes.read_text() if notes.exists() else None, contents)


class ContainerReleaseTest(unittest.TestCase):
    def test_container_configuration_and_order(self):
        workflow = WORKFLOW.read_text()
        self.assertIn('tags: ["v*"]', workflow)
        self.assertIn('permissions:\n  contents: write', workflow)
        self.assertIn('    permissions:\n      contents: write\n      packages: write', workflow)
        steps = ["name: Validate release notes", "name: Test release workflow", "name: Test\n",
                 "name: Build release files", "name: Container tags", "docker/setup-qemu-action@v4",
                 "docker/setup-buildx-action@v4", "docker/login-action@v4",
                 "docker/build-push-action@v7", "name: Publish release"]
        for step in steps:
            self.assertIn(step, workflow)
        positions = [workflow.index(step) for step in steps]
        self.assertEqual(positions, sorted(positions))
        self.assertIn('RELEASE_TAG: ${{ github.ref_name }}', workflow)
        self.assertIn('registry: ghcr.io', workflow)
        self.assertIn('username: ${{ github.actor }}', workflow)
        self.assertIn('password: ${{ github.token }}', workflow)
        self.assertNotIn('secrets.', workflow)
        for line in ('context: .', 'file: ./Dockerfile', 'platforms: linux/amd64,linux/arm64',
                     'push: true', 'tags: ${{ steps.container-tags.outputs.tags }}'):
            self.assertIn(line, workflow)
        self.assertIn('id: container-tags', workflow)
        self.assertIn('cp deploy/up.service dist/up.service', workflow)
        self.assertIn('cp deploy/up-system.service dist/up-system.service', workflow)
        self.assertIn('sha256sum up_linux_*.tar.gz > checksums.txt', workflow)
        self.assertIn('for arch in amd64 arm64; do', workflow)

    def test_container_tags(self):
        workflow = WORKFLOW.read_text()
        self.assertIn('name: Container tags', workflow)
        block = run_block(workflow, 'Container tags')
        self.assertNotIn('${{', block)
        image = 'ghcr.io/heohlee/up'
        for tag, latest in [('v1.2.3', True), ('v0.0.0', True), ('v1.2.3-rc.1', False),
                            ('v1.2', False), ('v1.2.3.4', False), ('vabc', False),
                            ('v1.2.3+build.1', True), ('v1.2.3-rc.1+build.1', False),
                            ('v' + 'a' * 130, False)]:
            with self.subTest(tag=tag), tempfile.TemporaryDirectory() as scratch:
                output = Path(scratch) / 'output'
                output.touch()
                env = dict(os.environ, RELEASE_TAG=tag, GITHUB_OUTPUT=str(output))
                result = subprocess.run(['bash', '--noprofile', '--norc', '-e', '-o', 'pipefail', '-c', block],
                                        cwd=scratch, env=env, capture_output=True, text=True)
                self.assertEqual((result.returncode, result.stdout, result.stderr), (0, '', ''))
                image_tag = tag.replace('+', '-')[:128]
                expected = f'tags<<EOF\n{image}:{image_tag}\n' + (f'{image}:latest\n' if latest else '') + 'EOF\n'
                self.assertEqual(output.read_text(), expected)
                self.assertRegex(output.read_text().splitlines()[1].split(':')[1], r'^[\w][\w.-]{0,127}$')

    def test_rejected_notes_do_not_publish_or_change_files(self):
        workflow = WORKFLOW.read_text()
        self.assertIn('name: Container tags', workflow)
        validate = run_block(workflow, 'Validate release notes')
        tags = run_block(workflow, 'Container tags')
        publish = run_block(workflow, 'Publish release')
        for contents in (None, '', ' \n\t\n', '\u00a0\n'):
            with self.subTest(contents=contents), tempfile.TemporaryDirectory() as scratch:
                root = Path(scratch)
                notes = root / '.github/release-notes/v9.9.9.md'
                notes.parent.mkdir(parents=True)
                if contents is not None:
                    notes.write_text(contents)
                for command in ('gh', 'docker'):
                    stub = root / command
                    stub.write_text('#!/bin/bash\necho called >> "$CALL_LOG"\n')
                    stub.chmod(0o755)
                output = root / 'output'
                output.write_text('unchanged\n')
                log = root / 'calls'
                env = dict(os.environ, RELEASE_TAG='v9.9.9', GITHUB_OUTPUT=str(output),
                           CALL_LOG=str(log), PATH=f"{root}:{os.environ['PATH']}")
                result = subprocess.run(['bash', '--noprofile', '--norc', '-e', '-o', 'pipefail', '-c',
                                         validate + '\n' + tags + '\ndocker push example\n' + publish],
                                        cwd=root, env=env, capture_output=True, text=True)
                self.assertEqual((result.returncode, result.stdout, result.stderr),
                                 (1, '', '::error::Release notes must exist and contain non-whitespace text: .github/release-notes/v9.9.9.md\n'))
                self.assertFalse(log.exists())
                self.assertEqual(output.read_text(), 'unchanged\n')
                self.assertEqual(notes.read_text() if notes.exists() else None, contents)


if __name__ == "__main__":
    unittest.main()
